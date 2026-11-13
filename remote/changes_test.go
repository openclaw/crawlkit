package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func changeText(value string) ChangeValue    { return ChangeValue{Kind: "text", Value: &value} }
func changeInteger(value string) ChangeValue { return ChangeValue{Kind: "integer", Value: &value} }
func changeFixture() ChangeBatch {
	return ChangeBatch{Contract: ChangeContract, App: "fixture", Archive: "team/archive", BindingID: "registered-binding",
		CaptureID: "0123456789abcdef0123456789abcdef", PartCount: 1, NativeFrom: "generation:0:anchor", NativeThrough: "generation:3:next",
		Changes: []Change{{Table: "messages", Operation: "upsert", Key: []ChangeValue{changeText("C1"), changeText("1.000001")},
			Row: []ChangeValue{changeText("☃\x00tail"), changeText(""), {Kind: "null"}, changeInteger("9007199254740993"), changeInteger("-9223372036854775808")}},
			{Table: "messages", Operation: "remove", Key: []ChangeValue{changeText("C1"), changeText("2.000002")}}}}
}
func changeBody(t *testing.T, batch ChangeBatch) []byte {
	t.Helper()
	body, err := EncodeChangeBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func changeClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: tokenProviderFunc(func(context.Context) (string, error) { return "fixture-token", nil })})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func matchingChangeReceipt(batch ChangeBatch, body []byte) ChangeReceipt {
	digest := sha256.Sum256(body)
	return ChangeReceipt{App: batch.App, Archive: batch.Archive, BindingID: batch.BindingID, BatchID: batch.BatchID(), PayloadSHA256: hex.EncodeToString(digest[:]), Sequence: "9007199254740993", Cursor: batch.BatchID()}
}

func TestApplyChangesPreservesPersistedBytes(t *testing.T) {
	batch := changeFixture()
	body := changeBody(t, batch)
	if again := changeBody(t, batch); !bytes.Equal(again, body) {
		t.Fatal("encoding is not deterministic")
	}
	// Persisted caller bytes can use different field order and whitespace.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	persisted, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	persisted = append(append([]byte("\n"), persisted...), '\n')
	original := bytes.Clone(persisted)
	calls := 0
	client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/apps/fixture/archives/team%2Farchive/changes" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong authenticated change route")
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if !bytes.Equal(got, persisted) || r.ContentLength != int64(len(persisted)) {
			t.Error("persisted bytes changed in transport")
		}
		var decoded ChangeBatch
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Error(err)
			return
		}
		if !reflect.DeepEqual(decoded, batch) {
			t.Error("wire values lost NULL/text/integer precision")
		}
		receipt := matchingChangeReceipt(batch, got)
		receipt.Replayed = calls == 2
		_ = json.NewEncoder(w).Encode(receipt)
	})
	for i := 0; i < 2; i++ {
		receipt, err := client.ApplyChanges(context.Background(), batch.App, batch.Archive, persisted)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Replayed != (i == 1) || receipt.Sequence != "9007199254740993" {
			t.Fatalf("wrong receipt: %+v", receipt)
		}
	}
	if calls != 2 || !bytes.Equal(persisted, original) {
		t.Fatal("client retried or changed caller bytes")
	}
}

func TestApplyChangesRejectsInvalidBodiesBeforeCredentials(t *testing.T) {
	valid := string(changeBody(t, changeFixture()))
	cases := map[string]string{
		"unknown":            strings.Replace(valid, `"contract":`, `"file_path":"private","contract":`, 1),
		"duplicate":          strings.Replace(valid, `"contract":`, `"contract":"changes-v1","contract":`, 1),
		"case alias":         strings.Replace(valid, `"contract":`, `"Contract":"changes-v1","contract":`, 1),
		"escaped duplicate":  strings.Replace(valid, `"contract":`, `"contr\u0061ct":"changes-v1","contract":`, 1),
		"missing field":      strings.Replace(valid, `"expected_cursor":"",`, "", 1),
		"null scalar":        strings.Replace(valid, `{"kind":"null"}`, `{"kind":"null","value":null}`, 1),
		"null text":          strings.Replace(valid, `{"kind":"text","value":""}`, `{"kind":"text","value":null}`, 1),
		"missing text":       strings.Replace(valid, `{"kind":"text","value":""}`, `{"kind":"text"}`, 1),
		"scalar unknown":     strings.Replace(valid, `{"kind":"null"}`, `{"kind":"null","path":"private"}`, 1),
		"scalar duplicate":   strings.Replace(valid, `{"kind":"null"}`, `{"kind":"null","kind":"null"}`, 1),
		"change unknown":     strings.Replace(valid, `"operation":"upsert"`, `"operation":"upsert","path":"private"`, 1),
		"change duplicate":   strings.Replace(valid, `"operation":"upsert"`, `"operation":"remove","operation":"upsert"`, 1),
		"unpaired surrogate": strings.Replace(valid, `"registered-binding"`, `"\ud800"`, 1),
		"invalid UTF8":       strings.Replace(valid, "registered-binding", string([]byte{0xff}), 1),
		"trailing object":    valid + `{}`,
		"trailing slash":     `{"contract":"\`,
		"short unicode":      `{"contract":"\u0"}`,
		"oversize":           strings.Repeat(" ", MaxChangeBytes) + valid,
	}
	// The array is required and must not be JSON null.
	var object map[string]json.RawMessage
	_ = json.Unmarshal([]byte(valid), &object)
	object["changes"] = json.RawMessage("null")
	nullChanges, _ := json.Marshal(object)
	cases["null changes"] = string(nullChanges)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			client, err := NewClient(Options{Endpoint: "https://remote.test", TokenProvider: tokenProviderFunc(func(context.Context) (string, error) { t.Fatal("invalid body reached credentials"); return "", nil })})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := client.ApplyChanges(context.Background(), "fixture", "team/archive", []byte(body))
			if err == nil || receipt != (ChangeReceipt{}) {
				t.Fatalf("invalid body accepted: %v", err)
			}
		})
	}
}

func TestApplyChangesReturnsConflictWithoutRetry(t *testing.T) {
	batch := changeFixture()
	body := changeBody(t, batch)
	calls := 0
	client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"cursor_conflict","message":"reconcile"}`)
	})
	receipt, err := client.ApplyChanges(context.Background(), batch.App, batch.Archive, body)
	var remoteErr *Error
	if !errors.As(err, &remoteErr) || remoteErr.Status != 409 || remoteErr.Code != "cursor_conflict" || receipt != (ChangeReceipt{}) || calls != 1 {
		t.Fatalf("conflict not preserved: %v, calls=%d", err, calls)
	}
	if _, err := client.ApplyChanges(context.Background(), "other", batch.Archive, body); err == nil || calls != 1 {
		t.Fatal("route mismatch reached HTTP")
	}
	for _, route := range BaseContract().Routes {
		if route.Path == ChangesRoute {
			t.Fatal("unimplemented receiver advertised")
		}
	}
}

func TestChangeBatchLimitsAndScalars(t *testing.T) {
	cases := map[string]func(*ChangeBatch){
		"contract":             func(b *ChangeBatch) { b.Contract = "v2" },
		"binding empty":        func(b *ChangeBatch) { b.BindingID = "" },
		"binding bytes":        func(b *ChangeBatch) { b.BindingID = strings.Repeat("☃", 43) },
		"from empty":           func(b *ChangeBatch) { b.NativeFrom = "" },
		"through empty":        func(b *ChangeBatch) { b.NativeThrough = "" },
		"cursor bytes":         func(b *ChangeBatch) { b.ExpectedCursor = strings.Repeat("c", 513) },
		"capture uppercase":    func(b *ChangeBatch) { b.CaptureID = strings.ToUpper(b.CaptureID) },
		"capture short":        func(b *ChangeBatch) { b.CaptureID = "abcd" },
		"negative part":        func(b *ChangeBatch) { b.PartIndex = -1 },
		"missing part":         func(b *ChangeBatch) { b.PartIndex = b.PartCount },
		"empty parts":          func(b *ChangeBatch) { b.PartCount = 0 },
		"too many parts":       func(b *ChangeBatch) { b.PartCount = 65 },
		"too many changes":     func(b *ChangeBatch) { b.Changes = make([]Change, 501) },
		"nil changes":          func(b *ChangeBatch) { b.Changes = nil },
		"missing key":          func(b *ChangeBatch) { b.Changes[0].Key = nil },
		"missing row":          func(b *ChangeBatch) { b.Changes[0].Row = nil },
		"removal row":          func(b *ChangeBatch) { b.Changes[1].Row = []ChangeValue{} },
		"unknown operation":    func(b *ChangeBatch) { b.Changes[0].Operation = "delete" },
		"unknown kind":         func(b *ChangeBatch) { b.Changes[0].Row[0].Kind = "blob" },
		"null value":           func(b *ChangeBatch) { b.Changes[0].Row[2] = ChangeValue{Kind: "null", Value: changeText("").Value} },
		"integer leading zero": func(b *ChangeBatch) { b.Changes[0].Row[3] = changeInteger("01") },
		"integer minus zero":   func(b *ChangeBatch) { b.Changes[0].Row[3] = changeInteger("-0") },
		"integer overflow":     func(b *ChangeBatch) { b.Changes[0].Row[3] = changeInteger("9223372036854775808") },
		"integer fractional":   func(b *ChangeBatch) { b.Changes[0].Row[3] = changeInteger("1.0") },
		"invalid UTF8":         func(b *ChangeBatch) { b.Changes[0].Row[0] = changeText(string([]byte{0xff})) },
		"text oversized":       func(b *ChangeBatch) { b.Changes[0].Row[0] = changeText(strings.Repeat("x", MaxChangeBytes)) },
		"escaping oversized":   func(b *ChangeBatch) { b.Changes[0].Row[0] = changeText(strings.Repeat("\x00", MaxChangeBytes/5)) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			batch := changeFixture()
			mutate(&batch)
			if _, err := EncodeChangeBatch(batch); err == nil {
				t.Fatal("invalid batch encoded")
			}
		})
	}
	batch := changeFixture()
	batch.PartCount, batch.PartIndex = 64, 63
	batch.BindingID = strings.Repeat("b", 128)
	batch.NativeThrough = strings.Repeat("c", 512)
	batch.Changes = []Change{}
	changeBody(t, batch) // Empty observations can advance a validated native interval.
	batch.Changes = make([]Change, 500)
	for i := range batch.Changes {
		batch.Changes[i] = Change{Table: "messages", Operation: "remove", Key: []ChangeValue{changeInteger("0")}}
	}
	changeBody(t, batch)
}

func TestApplyChangesRejectsMismatchedReceipt(t *testing.T) {
	batch := changeFixture()
	body := changeBody(t, batch)
	cases := map[string]func(*ChangeReceipt){
		"app":               func(r *ChangeReceipt) { r.App = "other" },
		"archive":           func(r *ChangeReceipt) { r.Archive = "other" },
		"binding":           func(r *ChangeReceipt) { r.BindingID = "other" },
		"batch":             func(r *ChangeReceipt) { r.BatchID += "x" },
		"digest":            func(r *ChangeReceipt) { r.PayloadSHA256 = strings.Repeat("0", 64) },
		"cursor":            func(r *ChangeReceipt) { r.Cursor = "other" },
		"zero sequence":     func(r *ChangeReceipt) { r.Sequence = "0" },
		"negative sequence": func(r *ChangeReceipt) { r.Sequence = "-1" },
		"leading zero":      func(r *ChangeReceipt) { r.Sequence = "01" },
		"plus sequence":     func(r *ChangeReceipt) { r.Sequence = "+1" },
		"overflow sequence": func(r *ChangeReceipt) { r.Sequence = "9223372036854775808" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
				receipt := matchingChangeReceipt(batch, body)
				mutate(&receipt)
				_ = json.NewEncoder(w).Encode(receipt)
			})
			receipt, err := client.ApplyChanges(context.Background(), batch.App, batch.Archive, body)
			if err == nil || receipt != (ChangeReceipt{}) {
				t.Fatalf("mismatched receipt accepted: %v", err)
			}
		})
	}
	for _, invalid := range []string{`"sequence":null`, `"sequence":1`, `"sequence":"1","sequence":"2"`, `"sequence":"1","unknown":true`, `"sequence":"1","replayed":null`} {
		t.Run(invalid, func(t *testing.T) {
			response, _ := json.Marshal(matchingChangeReceipt(batch, body))
			response = bytes.Replace(response, []byte(`"sequence":"9007199254740993"`), []byte(invalid), 1)
			client := changeClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(response) })
			if receipt, err := client.ApplyChanges(context.Background(), batch.App, batch.Archive, body); err == nil || receipt != (ChangeReceipt{}) {
				t.Fatal("ambiguous receipt accepted")
			}
		})
	}
}

func TestApplyChangesBodyByteBoundary(t *testing.T) {
	batch := changeFixture()
	batch.Changes[0].Row[0] = changeText("")
	base := changeBody(t, batch)
	batch.Changes[0].Row[0] = changeText(strings.Repeat("x", MaxChangeBytes-len(base)))
	body := changeBody(t, batch)
	if len(body) != MaxChangeBytes {
		t.Fatalf("boundary body has %d bytes", len(body))
	}
	calls := 0
	client := changeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, body) {
			t.Error("boundary request changed")
			return
		}
		receipt := matchingChangeReceipt(batch, got)
		receipt.Sequence = "9223372036854775807"
		_ = json.NewEncoder(w).Encode(receipt)
	})
	if _, err := client.ApplyChanges(context.Background(), batch.App, batch.Archive, body); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyChanges(context.Background(), batch.App, batch.Archive, append(body, ' ')); err == nil || calls != 1 {
		t.Fatal("over-limit valid JSON reached HTTP")
	}
}
