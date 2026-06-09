package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type row struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

func TestWriteReadEncryptedSnapshot(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	recipientFromIdentity, err := RecipientFromIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if recipientFromIdentity != recipient {
		t.Fatalf("recipient from identity = %q, want %q", recipientFromIdentity, recipient)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	if err := os.MkdirAll(cfg.Repo, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := WriteSnapshot(context.Background(), cfg, []Shard{
		{Table: "messages", Path: "data/messages/2026/05.jsonl.gz.age", Rows: []row{{ID: "1", Body: "hello"}}},
	}, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Counts["messages"] != 1 || len(manifest.Shards) != 1 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	stored, err := ReadManifest(cfg.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Counts["messages"] != 1 || len(stored.Shards) != 1 {
		t.Fatalf("unexpected stored manifest: %+v", stored)
	}
	decoded, err := ReadSnapshot(cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var rows []row
	if err := DecodeJSONL(decoded[0].Plaintext, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Body != "hello" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestResolveShardPathRejectsEscapes(t *testing.T) {
	for _, rel := range []string{"../x.age", "data/../x.age", "data/x.txt", "/data/x.age"} {
		if _, err := ResolveShardPath(t.TempDir(), rel); err == nil {
			t.Fatalf("expected error for %q", rel)
		}
	}
}
