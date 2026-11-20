package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryResultsPreserveJSONNumbers(t *testing.T) {
	const resultJSON = `{"columns":["id","amount"],"rows":[[9007199254740993,0.1234567890123456789]],"values":[{"id":9007199254740993,"amount":0.1234567890123456789,"nested":[-9007199254740993,1.234567890123456789e30]}],"stats":{"rows_read":9007199254740993}}`
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if batch {
					fmt.Fprint(w, `{"results":[`+resultJSON+`]}`)
				} else {
					fmt.Fprint(w, resultJSON)
				}
			}))
			defer server.Close()
			client := newTestClient(t, Options{Endpoint: server.URL, TokenProvider: StaticToken("fixture")})
			request := QueryRequest{Name: "fixture.search"}
			var result QueryResult
			if batch {
				results, err := client.BatchRead(context.Background(), "fixture", "archive", []QueryRequest{request})
				if err != nil || len(results) != 1 {
					t.Fatalf("batch query = %v, %v", results, err)
				}
				result = results[0]
			} else {
				var err error
				result, err = client.Query(context.Background(), "fixture", "archive", request)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(result.Rows) != 1 || len(result.Rows[0]) != 2 || len(result.Values) != 1 {
				t.Fatalf("unexpected query result: %#v", result)
			}
			nested, ok := result.Values[0]["nested"].([]any)
			if !ok || len(nested) != 2 {
				t.Fatalf("unexpected nested values: %#v", result.Values)
			}
			for _, check := range []struct {
				value any
				want  string
			}{
				{result.Rows[0][0], "9007199254740993"},
				{result.Rows[0][1], "0.1234567890123456789"},
				{result.Values[0]["id"], "9007199254740993"},
				{result.Values[0]["amount"], "0.1234567890123456789"},
				{nested[0], "-9007199254740993"},
				{nested[1], "1.234567890123456789e30"},
			} {
				number, ok := check.value.(json.Number)
				if !ok || number.String() != check.want {
					t.Errorf("number = %v (%T), want exact token %s", check.value, check.value, check.want)
				}
			}
			if result.Stats.RowsRead != 9007199254740993 {
				t.Errorf("typed rows_read = %d", result.Stats.RowsRead)
			}
		})
	}
}
