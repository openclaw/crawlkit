package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// These describe an optional protocol, not a deployed receiver capability.
	ChangeContract       = "changes-v1"
	ChangesRoute         = "/v1/apps/:app/archives/:archive/changes"
	MaxChangeBytes       = 4 << 20
	MaxChanges           = 500
	MaxChangeParts       = 64
	maxChangeIDBytes     = 128
	maxChangeCursorBytes = 512
)

// ChangeBatch contains explicitly projected service data, never native paths,
// file identities or private admission metadata. BindingID identifies an
// immutable approved scope/profile/baseline; it does not grant authorization.
type ChangeBatch struct {
	Contract       string   `json:"contract"`
	App            string   `json:"app"`
	Archive        string   `json:"archive"`
	BindingID      string   `json:"binding_id"`
	CaptureID      string   `json:"capture_id"`
	PartIndex      int      `json:"part_index"`
	PartCount      int      `json:"part_count"`
	NativeFrom     string   `json:"native_from"`
	NativeThrough  string   `json:"native_through"`
	ExpectedCursor string   `json:"expected_cursor"`
	Changes        []Change `json:"changes"`
}

type Change struct {
	Table     string        `json:"table"`
	Operation string        `json:"operation"`
	Key       []ChangeValue `json:"key"`
	Row       []ChangeValue `json:"row,omitempty"`
}

// ChangeValue preserves NULL, empty text and exact signed 64-bit integers.
// Kind is null/text/integer; Value is absent only for null. Integers use decimal
// strings so JavaScript intermediaries cannot round them.
type ChangeValue struct {
	Kind  string  `json:"kind"`
	Value *string `json:"value,omitempty"`
}

type ChangeReceipt struct {
	App           string `json:"app"`
	Archive       string `json:"archive"`
	BindingID     string `json:"binding_id"`
	BatchID       string `json:"batch_id"`
	PayloadSHA256 string `json:"payload_sha256"`
	Sequence      string `json:"sequence"`
	Cursor        string `json:"cursor"`
	Replayed      bool   `json:"replayed"`
}

// BatchID also supplies the next archive cursor. Validate the batch before use.
func (b ChangeBatch) BatchID() string {
	return "chg1:" + b.CaptureID + ":" + strconv.Itoa(b.PartIndex)
}

// EncodeChangeBatch is a deterministic sender convenience. Receivers hash exact
// persisted request bytes, not a re-encoding or normalized JSON value.
func EncodeChangeBatch(batch ChangeBatch) ([]byte, error) {
	if err := validateChangeBatch(batch); err != nil {
		return nil, err
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxChangeBytes {
		return nil, errors.New("change body exceeds 4 MiB")
	}
	return body, nil
}

// ApplyChanges sends persisted bytes unchanged and verifies their receipt. The
// caller owns capability discovery, durable outbox/retries and final native
// acknowledgment. This method never falls back to snapshot ingestion.
func (c *Client) ApplyChanges(ctx context.Context, app, archive string, body []byte) (ChangeReceipt, error) {
	batch, err := decodeChangeBatch(body)
	if err != nil {
		return ChangeReceipt{}, err
	}
	if batch.App != strings.TrimSpace(app) || batch.Archive != strings.TrimSpace(archive) {
		return ChangeReceipt{}, errors.New("change app/archive must match the route")
	}
	digest := sha256.Sum256(body)
	var response json.RawMessage
	if err := c.doRaw(ctx, http.MethodPost, archivePath(app, archive, "changes"), bytes.NewReader(body), int64(len(body)), nil, &response); err != nil {
		return ChangeReceipt{}, err
	}
	receipt, err := decodeChangeReceipt(response)
	if err != nil {
		return ChangeReceipt{}, err
	}
	if receipt.App != batch.App || receipt.Archive != batch.Archive || receipt.BindingID != batch.BindingID || receipt.BatchID != batch.BatchID() || receipt.Cursor != batch.BatchID() || receipt.PayloadSHA256 != hex.EncodeToString(digest[:]) {
		return ChangeReceipt{}, errors.New("change receipt does not match the persisted batch")
	}
	sequence, err := strconv.ParseInt(receipt.Sequence, 10, 64)
	if err != nil || sequence <= 0 || strconv.FormatInt(sequence, 10) != receipt.Sequence {
		return ChangeReceipt{}, errors.New("change receipt sequence must be a positive canonical int64")
	}
	return receipt, nil
}

func changeMetadata(value string, limit int, empty bool) bool {
	return (empty || value != "") && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validateChangeBatch(batch ChangeBatch) error {
	if batch.Contract != ChangeContract {
		return errors.New("unsupported change contract")
	}
	for _, value := range []string{batch.App, batch.Archive, batch.BindingID} {
		if !changeMetadata(value, maxChangeIDBytes, false) {
			return errors.New("change app/archive/binding must be nonempty metadata of at most 128 UTF-8 bytes")
		}
	}
	id, err := hex.DecodeString(batch.CaptureID)
	if err != nil || len(id) != 16 || strings.ToLower(batch.CaptureID) != batch.CaptureID {
		return errors.New("change capture ID must be 32 lowercase hexadecimal characters")
	}
	if batch.PartCount < 1 || batch.PartCount > MaxChangeParts || batch.PartIndex < 0 || batch.PartIndex >= batch.PartCount {
		return errors.New("change part index/count is outside the supported range")
	}
	if !changeMetadata(batch.NativeFrom, maxChangeCursorBytes, false) || !changeMetadata(batch.NativeThrough, maxChangeCursorBytes, false) || !changeMetadata(batch.ExpectedCursor, maxChangeCursorBytes, true) {
		return errors.New("change native anchors must be nonempty; cursors must be at most 512 UTF-8 bytes")
	}
	if batch.Changes == nil || len(batch.Changes) > MaxChanges {
		return errors.New("changes must be an array of at most 500 observations")
	}
	// Bound input before marshaling; escaping can expand it, so encoded bytes
	// are checked separately. Neither limit is an RSS or durable outbox budget.
	textBytes := 0
	for _, change := range batch.Changes {
		if !changeMetadata(change.Table, maxChangeIDBytes, false) || len(change.Key) == 0 {
			return errors.New("change table and key are required")
		}
		switch change.Operation {
		case "upsert":
			if len(change.Row) == 0 {
				return errors.New("change upsert requires a row")
			}
		case "remove":
			if change.Row != nil {
				return errors.New("change removal must omit its row")
			}
		default:
			return errors.New("change operation must be upsert or remove")
		}
		for _, values := range [][]ChangeValue{change.Key, change.Row} {
			for _, value := range values {
				if err := validateChangeValue(value); err != nil {
					return err
				}
				textBytes += len(value.Kind) + 1
				if value.Value != nil {
					if len(*value.Value) > MaxChangeBytes-textBytes {
						return errors.New("change values exceed 4 MiB")
					}
					textBytes += len(*value.Value)
				}
				if textBytes > MaxChangeBytes {
					return errors.New("change values exceed 4 MiB")
				}
			}
		}
	}
	return nil
}

func validateChangeValue(value ChangeValue) error {
	if value.Kind == "null" && value.Value == nil {
		return nil
	}
	if value.Value == nil || !utf8.ValidString(*value.Value) {
		return errors.New("change text/integer requires a valid UTF-8 value")
	}
	switch value.Kind {
	case "text":
		return nil
	case "integer":
		number, err := strconv.ParseInt(*value.Value, 10, 64)
		if err == nil && strconv.FormatInt(number, 10) == *value.Value {
			return nil
		}
	}
	return errors.New("invalid change scalar kind or integer")
}
