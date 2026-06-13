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

func TestWriteSnapshotHonorsCanceledContext(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = WriteSnapshot(ctx, cfg, []Shard{
		{Table: "messages", Path: "data/messages/2026/05.jsonl.gz.age", Rows: []row{{ID: "1", Body: "hello"}}},
	}, Manifest{})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.Repo, "manifest.json")); !os.IsNotExist(statErr) {
		t.Fatalf("manifest stat err = %v", statErr)
	}
}

func TestWriteShardVersionsChangedExistingManifestPath(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	rel := "data/messages/2026/05.jsonl.gz.age"
	oldPath, err := ResolveShardPath(cfg.Repo, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("old encrypted shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := Manifest{Shards: []ShardEntry{{Table: "messages", Path: rel, SHA256: "old-hash"}}}
	entry, err := writeShard(context.Background(), cfg, old, "messages", rel, []byte("new plaintext\n"), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path == rel {
		t.Fatalf("changed shard reused old path %q", rel)
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old encrypted shard" {
		t.Fatalf("old shard was overwritten: %q", data)
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, filepath.FromSlash(entry.Path))); err != nil {
		t.Fatalf("new shard missing: %v", err)
	}

	sameHashOld := Manifest{Shards: []ShardEntry{{Table: "messages", Path: rel, SHA256: SHA256Hex([]byte("new plaintext\n"))}}}
	entry, err = writeShard(context.Background(), cfg, sameHashOld, "messages", rel, []byte("new plaintext\n"), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path == rel {
		t.Fatalf("re-encrypted shard reused old path %q", rel)
	}
}

func TestPublicBackupHelpers(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	plaintext, rows, err := EncodeJSONL([]row{{ID: "1", Body: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 || len(plaintext) == 0 {
		t.Fatalf("encoded rows=%d bytes=%d", rows, len(plaintext))
	}
	ciphertext, hash, err := EncryptShard(plaintext, cfg.Recipients)
	if err != nil {
		t.Fatal(err)
	}
	if hash != SHA256Hex(plaintext) || len(ciphertext) == 0 {
		t.Fatalf("hash=%q ciphertext=%d", hash, len(ciphertext))
	}
	entry, err := WriteShard(cfg, Manifest{}, "messages", "data/messages/2026/05.jsonl.gz.age", plaintext, rows, false)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path == "" || entry.SHA256 != hash {
		t.Fatalf("entry = %+v", entry)
	}
	if err := WriteManifest(cfg.Repo, Manifest{Format: FormatVersion, Encrypted: true, Shards: []ShardEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(cfg.Repo); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cfg.Repo, "data", "messages", "stale.age")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStaleShards(cfg.Repo, []ShardEntry{entry}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale shard stat err = %v", err)
	}
}

func TestResolveShardPathRejectsEscapes(t *testing.T) {
	for _, rel := range []string{"../x.age", "data/../x.age", "data/x.txt", "/data/x.age"} {
		if _, err := ResolveShardPath(t.TempDir(), rel); err == nil {
			t.Fatalf("expected error for %q", rel)
		}
	}
}
