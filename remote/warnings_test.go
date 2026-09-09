package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

func TestArchiveWarningsJSONCompatibility(t *testing.T) {
	warnings := []string{"source note", "note with \"quotes\"\nand a newline", "source note"}
	for _, tc := range []struct {
		name         string
		legacyJSON   string
		without      any
		empty        any
		withWarnings any
		newValue     func() any
	}{
		{
			name:         "manifest",
			legacyJSON:   `{"app":"example","archive":"sample","schema_version":1,"schema_hash":"hash"}`,
			without:      &IngestManifest{App: "example", Archive: "sample", SchemaVersion: 1, SchemaHash: "hash"},
			empty:        &IngestManifest{App: "example", Archive: "sample", SchemaVersion: 1, SchemaHash: "hash", Warnings: []string{}},
			withWarnings: &IngestManifest{App: "example", Archive: "sample", SchemaVersion: 1, SchemaHash: "hash", Warnings: warnings},
			newValue:     func() any { return &IngestManifest{} },
		},
		{
			name:         "snapshot",
			legacyJSON:   `{"id":"snapshot-1","schema_version":1,"coverage_complete":true}`,
			without:      &ArchiveSnapshot{ID: "snapshot-1", SchemaVersion: 1, CoverageComplete: true},
			empty:        &ArchiveSnapshot{ID: "snapshot-1", SchemaVersion: 1, CoverageComplete: true, Warnings: []string{}},
			withWarnings: &ArchiveSnapshot{ID: "snapshot-1", SchemaVersion: 1, CoverageComplete: true, Warnings: warnings},
			newValue:     func() any { return &ArchiveSnapshot{} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy := tc.newValue()
			if err := json.Unmarshal([]byte(tc.legacyJSON), legacy); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(legacy, tc.without) {
				t.Fatalf("legacy decode = %#v, want %#v", legacy, tc.without)
			}
			for _, value := range []any{tc.without, tc.empty} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != tc.legacyJSON {
					t.Fatalf("empty warnings changed legacy JSON: %s", data)
				}
			}

			data, err := json.Marshal(tc.withWarnings)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			var gotWarnings []string
			if err := json.Unmarshal(fields["warnings"], &gotWarnings); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(gotWarnings, warnings) {
				t.Fatalf("warnings = %#v, want %#v", gotWarnings, warnings)
			}
			delete(fields, "warnings")
			var legacyFields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.legacyJSON), &legacyFields); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fields, legacyFields) {
				t.Fatalf("warnings changed existing fields: %#v", fields)
			}
			decoded := tc.newValue()
			if err := json.Unmarshal(data, decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, tc.withWarnings) {
				t.Fatalf("roundtrip = %#v, want %#v", decoded, tc.withWarnings)
			}
		})
	}
}

func TestClientTransportsArchiveWarnings(t *testing.T) {
	warnings := []string{"source note", "additional note"}
	var received IngestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/apps/example/archives/sample/ingest":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(IngestResult{Complete: true})
		case "/v1/apps/example/archives/sample/status":
			_ = json.NewEncoder(w).Encode(Status{
				App:      "example",
				Archive:  "sample",
				Warnings: []string{"request note"},
				Snapshot: &ArchiveSnapshot{ID: "snapshot-1", Warnings: warnings},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Ingest(context.Background(), "example", "sample", IngestRequest{
		Manifest: IngestManifest{Warnings: warnings},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || received.Manifest.App != "example" || received.Manifest.Archive != "sample" ||
		!slices.Equal(received.Manifest.Warnings, warnings) {
		t.Fatalf("ingest = %#v, manifest = %#v", result, received.Manifest)
	}
	status, err := client.Status(context.Background(), "example", "sample")
	if err != nil {
		t.Fatal(err)
	}
	if status.Snapshot == nil || status.Snapshot.ID != "snapshot-1" ||
		!slices.Equal(status.Snapshot.Warnings, warnings) ||
		!slices.Equal(status.Warnings, []string{"request note"}) {
		t.Fatalf("status = %#v, snapshot = %#v", status, status.Snapshot)
	}
}
