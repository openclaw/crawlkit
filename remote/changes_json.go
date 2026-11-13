package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/openclaw/crawlkit/internal/jsontext"
)

// DecodeChangeBatch validates a bounded changes-v1 request without changing its
// bytes. Receivers must hash the original body and separately enforce route,
// publisher, binding and profile authorization before applying it.
func DecodeChangeBatch(body []byte) (ChangeBatch, error) {
	if len(body) > MaxChangeBytes {
		return ChangeBatch{}, errors.New("change body exceeds 4 MiB")
	}
	var batch ChangeBatch
	fields, err := decodeChangeObject(body, "contract app archive binding_id capture_id part_index part_count native_from native_through expected_cursor changes", "", &batch)
	if err != nil {
		return ChangeBatch{}, err
	}
	// decodeChangeObject proved JSON syntax before the text-only validator.
	if err := jsontext.Validate(body); err != nil {
		return ChangeBatch{}, err
	}
	var changes []json.RawMessage
	if err := json.Unmarshal(fields["changes"], &changes); err != nil {
		return ChangeBatch{}, err
	}
	for _, raw := range changes {
		var change Change
		fields, err := decodeChangeObject(raw, "table operation key", "row", &change)
		if err != nil {
			return ChangeBatch{}, err
		}
		for _, name := range []string{"key", "row"} {
			if data, ok := fields[name]; ok {
				var values []json.RawMessage
				if err := json.Unmarshal(data, &values); err != nil {
					return ChangeBatch{}, err
				}
				for _, data := range values {
					var value ChangeValue
					if _, err := decodeChangeObject(data, "kind", "value", &value); err != nil {
						return ChangeBatch{}, err
					}
				}
			}
		}
	}
	if err := validateChangeBatch(batch); err != nil {
		return ChangeBatch{}, err
	}
	return batch, nil
}

func decodeChangeReceipt(body []byte) (ChangeReceipt, error) {
	var receipt ChangeReceipt
	if len(body) > MaxChangeBytes {
		return receipt, errors.New("change receipt exceeds 4 MiB")
	}
	if _, err := decodeChangeObject(body, "app archive binding_id batch_id payload_sha256 sequence cursor replayed", "", &receipt); err != nil {
		return ChangeReceipt{}, err
	}
	if err := jsontext.Validate(body); err != nil {
		return ChangeReceipt{}, err
	}
	return receipt, nil
}

// These fixed protocol objects have no JSON-null fields. Null scalars use
// {"kind":"null"}; a present null value/row/array would erase presence evidence.
// Exact field names avoid encoding/json's case-insensitive aliases. This decoder
// preserves request bytes; field order and whitespace are not canonicalized.
func decodeChangeObject(body []byte, required, optional string, target any) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("change payload requires an object")
	}
	fields := map[string]json.RawMessage{}
	allowed := " " + required + " " + optional + " "
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid change JSON field")
		}
		name, ok := token.(string)
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") || !strings.Contains(allowed, " "+name+" ") {
			return nil, errors.New("unknown change JSON field")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("duplicate change JSON field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid change JSON value")
		}
		if bytes.Equal(value, []byte("null")) {
			return nil, errors.New("change fields must not be JSON null")
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid change JSON object")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("change JSON must contain one object")
	}
	for _, name := range strings.Fields(required) {
		if _, ok := fields[name]; !ok {
			return nil, errors.New("required change JSON field is missing")
		}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return nil, errors.New("invalid change JSON field type")
	}
	return fields, nil
}
