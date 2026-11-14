package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type receiptResponseBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *receiptResponseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *receiptResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestApplyChangesBoundsCompleteReceiptBody(t *testing.T) {
	batch := changeFixture()
	body := changeBody(t, batch)
	want := matchingChangeReceipt(batch, body)
	valid, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, response string
		valid          bool
	}{
		{name: "exact limit", response: strings.Repeat(" ", MaxChangeBytes-len(valid)) + string(valid), valid: true},
		{name: "oversize whitespace", response: strings.Repeat(" ", MaxChangeBytes) + string(valid)},
		{name: "oversize JSON value", response: `{"app":"` + strings.Repeat("x", 2*MaxChangeBytes) + `"}`},
		{name: "trailing object", response: string(valid) + `{}`},
		{name: "trailing garbage", response: string(valid) + `invalid`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &receiptResponseBody{Reader: strings.NewReader(tc.response)}
			client, err := NewClient(Options{
				Endpoint: "https://remote.test",
				TokenProvider: tokenProviderFunc(func(context.Context) (string, error) {
					return "fixture-token", nil
				}),
				HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: response}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := client.ApplyChanges(t.Context(), batch.App, batch.Archive, body)
			if tc.valid {
				if err != nil || receipt != want {
					t.Fatalf("valid receipt = %+v, %v", receipt, err)
				}
			} else if err == nil || receipt != (ChangeReceipt{}) {
				t.Fatalf("invalid receipt = %+v, %v", receipt, err)
			}
			if response.read > MaxChangeBytes+1 || !response.closed {
				t.Fatalf("response read %d bytes, closed=%v", response.read, response.closed)
			}
		})
	}
}
