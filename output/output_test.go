package output

import (
	"bytes"
	"testing"
)

func TestResolveJSONFlagWins(t *testing.T) {
	got, err := Resolve("text", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != JSON {
		t.Fatalf("format = %q", got)
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, JSON, "status", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"ok": true`)) {
		t.Fatalf("json output = %s", buf.String())
	}
}

func TestWriteLogRejectsAmbiguousLabels(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, Log, "status.ok-1", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("status.ok-1=")) {
		t.Fatalf("log output = %s", buf.String())
	}
	for _, label := range []string{"bad\nlabel", "bad=label"} {
		if err := Write(&buf, Log, label, map[string]any{"ok": true}); !IsUsage(err) {
			t.Fatalf("Write label %q error = %v, want usage error", label, err)
		}
	}
}

func TestUsageError(t *testing.T) {
	_, err := Resolve("xml", false)
	if !IsUsage(err) {
		t.Fatalf("expected usage error, got %v", err)
	}
}
