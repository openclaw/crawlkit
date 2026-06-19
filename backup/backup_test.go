package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/mirror"
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

func TestWriteSnapshotSupportsStableCountKeys(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	manifest, err := WriteSnapshot(context.Background(), cfg, []Shard{
		{Table: "group_participants", CountKey: "participants", Path: "data/participants.jsonl.gz.age", Rows: []row{{ID: "1"}}},
	}, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Counts["participants"] != 1 {
		t.Fatalf("stable count key missing: %+v", manifest.Counts)
	}
	if _, ok := manifest.Counts["group_participants"]; ok {
		t.Fatalf("table name leaked into counts: %+v", manifest.Counts)
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
	rotated := entry
	entry, err = writeShard(context.Background(), cfg, Manifest{Shards: []ShardEntry{rotated}}, "messages", rel, []byte("new plaintext\n"), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if entry != rotated {
		t.Fatalf("unchanged versioned shard was rewritten: got %+v, want %+v", entry, rotated)
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
	for _, rel := range []string{"../x.age", "data/../x.age", "data/x.txt", "/data/x.age", `data\files\index.jsonl.gz.age`, `data/files\index.jsonl.gz.age`} {
		if _, err := ResolveShardPath(t.TempDir(), rel); err == nil {
			t.Fatalf("expected error for %q", rel)
		}
	}
}

func TestHistoricalEncryptedSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	mirrorOpts := mirror.Options{RepoPath: cfg.Repo, Branch: "main"}
	if err := mirror.EnsureRepo(ctx, mirrorOpts); err != nil {
		t.Fatal(err)
	}
	mediaSource := filepath.Join(dir, "media.txt")
	if err := os.WriteFile(mediaSource, []byte("first media"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstManifest, err := WriteSnapshotWithFiles(ctx, cfg, []Shard{
		{Table: "messages", Path: "data/messages.jsonl.gz.age", Rows: []row{{ID: "1", Body: "first"}}},
	}, []File{{Path: "media/file.txt", Source: mediaSource}}, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if committed, err := mirror.Commit(ctx, mirrorOpts, "snapshot one"); err != nil || !committed {
		t.Fatalf("first commit = %v, %v", committed, err)
	}
	first, err := mirror.ResolveCommit(ctx, mirrorOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mirror.CreateImmutableTag(ctx, mirrorOpts, "snapshot/one"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaSource, []byte("second media"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondManifest, err := WriteSnapshotWithFiles(ctx, cfg, []Shard{
		{Table: "messages", Path: "data/messages.jsonl.gz.age", Rows: []row{{ID: "1", Body: "second"}}},
	}, []File{{Path: "media/file.txt", Source: mediaSource}}, firstManifest)
	if err != nil {
		t.Fatal(err)
	}
	if committed, err := mirror.Commit(ctx, mirrorOpts, "snapshot two"); err != nil || !committed {
		t.Fatalf("second commit = %v, %v", committed, err)
	}
	second, err := mirror.ResolveCommit(ctx, mirrorOpts, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	history, err := History(ctx, mirrorOpts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Ref != second || history[1].Ref != first || len(history[1].Tags) != 1 {
		t.Fatalf("history = %#v", history)
	}
	manifest, ref, err := ReadManifestAt(ctx, mirrorOpts, "snapshot/one")
	if err != nil {
		t.Fatal(err)
	}
	if ref != first || manifest.Counts["messages"] != 1 {
		t.Fatalf("manifest at ref = %#v, %s", manifest, ref)
	}
	decoded, resolved, err := ReadSnapshotAt(ctx, cfg, mirrorOpts, manifest, "snapshot/one")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != first || len(decoded) != 1 {
		t.Fatalf("decoded historical snapshot = %#v at %s", decoded, resolved)
	}
	var historicalRows []row
	if err := DecodeJSONL(decoded[0].Plaintext, &historicalRows); err != nil {
		t.Fatal(err)
	}
	if len(historicalRows) != 1 || historicalRows[0].Body != "first" {
		t.Fatalf("historical rows = %#v", historicalRows)
	}
	restoreRoot := filepath.Join(dir, "historical-restore")
	if err := os.MkdirAll(restoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	restored, restoredRef, err := RestoreFilesAt(ctx, cfg, mirrorOpts, manifest, "snapshot/one", restoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	if restored != 1 || restoredRef != first {
		t.Fatalf("historical files = %d at %s", restored, restoredRef)
	}
	mediaBody, err := os.ReadFile(filepath.Join(restoreRoot, "media", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mediaBody) != "first media" {
		t.Fatalf("historical media = %q", mediaBody)
	}
	if secondManifest.Counts["messages"] != 1 {
		t.Fatalf("second manifest = %#v", secondManifest)
	}
}
