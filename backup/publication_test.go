package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/internal/filelock"
)

func publicationConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
}

func rotatedConfig(t *testing.T, cfg Config) Config {
	t.Helper()
	next := publicationConfig(t)
	next.Repo = cfg.Repo
	return next
}

func publishedFiles(t *testing.T, repo string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.HasSuffix(path, ".lock") {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil {
			rel, relErr := filepath.Rel(repo, path)
			if relErr != nil {
				return relErr
			}
			files[rel] = data
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestSnapshotRotationFailurePreservesPublishedCiphertext(t *testing.T) {
	cfg := publicationConfig(t)
	shards := []Shard{{Table: "messages", Path: "data/messages/01.jsonl.gz.age", Rows: []row{{ID: "1", Body: "first"}}}}
	first, err := WriteSnapshot(context.Background(), cfg, shards, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	shards[0].Rows = []row{{ID: "1", Body: "changed"}}
	current, err := WriteSnapshot(context.Background(), cfg, shards, first)
	if err != nil {
		t.Fatal(err)
	}
	before := publishedFiles(t, cfg.Repo)
	next := rotatedConfig(t, cfg)
	failing := append(append([]Shard(nil), shards...), Shard{Table: "later", Path: "data/later.age", Rows: make(chan int)})
	if _, err := WriteSnapshot(context.Background(), next, failing, current); err == nil {
		t.Fatal("expected later encoding failure")
	}
	if !reflect.DeepEqual(before, publishedFiles(t, cfg.Repo)) {
		t.Error("failed rotation changed published bytes or leaked unpublished objects")
	}
	if _, err := ReadSnapshot(cfg, current); err != nil {
		t.Fatalf("old recipient cannot read current pack after failure: %v", err)
	}
	rotated, err := WriteSnapshot(context.Background(), next, shards, current)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Shards[0].Path == current.Shards[0].Path {
		t.Fatal("successful rotation reused physical ciphertext path")
	}
	if _, err := ReadSnapshot(next, rotated); err != nil {
		t.Fatalf("new recipient cannot read successful rotation: %v", err)
	}
	unchanged, err := WriteSnapshot(context.Background(), next, shards, rotated)
	if err != nil || !EquivalentManifest(rotated, unchanged) || !unchanged.Exported.Equal(rotated.Exported) {
		t.Fatalf("unchanged generation was rewritten: %+v, %v", unchanged, err)
	}
}

func TestSnapshotIndexRotationManifestFailurePreservesPublishedIndex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions")
	}
	cfg := publicationConfig(t)
	source := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(source, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []File{{Path: "media/item.bin", Source: source}}
	first, err := WriteSnapshotWithFiles(context.Background(), cfg, nil, files, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := WriteSnapshotWithFiles(context.Background(), cfg, nil, files, first)
	if err != nil {
		t.Fatal(err)
	}
	before := publishedFiles(t, cfg.Repo)
	if err := os.Chmod(cfg.Repo, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(cfg.Repo, 0o700)
	if _, err := WriteSnapshotWithFiles(context.Background(), rotatedConfig(t, cfg), nil, files, current); err == nil {
		t.Fatal("expected manifest promotion failure")
	}
	if !reflect.DeepEqual(before, publishedFiles(t, cfg.Repo)) {
		t.Error("failed rotation changed published bytes or leaked unpublished objects")
	}
	if _, err := RestoreFiles(context.Background(), cfg, current, t.TempDir()); err != nil {
		t.Fatalf("old recipient cannot restore prior files: %v", err)
	}
}

func TestSnapshotCleanupOnlyRemovesPriorManifestPaths(t *testing.T) {
	cfg := publicationConfig(t)
	shards := []Shard{{Table: "messages", Path: "data/messages.age", Rows: []row{{ID: "1"}}}}
	first, err := WriteSnapshot(context.Background(), cfg, shards, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	unowned := filepath.Join(cfg.Repo, "data", "unowned.age")
	if err := os.WriteFile(unowned, []byte("unowned"), 0o600); err != nil {
		t.Fatal(err)
	}
	shards[0].Rows = []row{{ID: "2"}}
	if _, err := WriteSnapshot(context.Background(), cfg, shards, first); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(unowned); err != nil || string(data) != "unowned" {
		t.Fatalf("cleanup removed unowned data: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, first.Shards[0].Path)); !os.IsNotExist(err) {
		t.Fatalf("obsolete owned ciphertext was not removed: %v", err)
	}
}

func TestSnapshotCleanupFailureReturnsCommittedManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions")
	}
	cfg := publicationConfig(t)
	first, err := WriteSnapshot(context.Background(), cfg, []Shard{
		{Table: "old", Path: "data/old/items.age", Rows: []row{{ID: "1"}}},
	}, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(cfg.Repo, "data", "old")
	if err := os.Chmod(oldDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(oldDir, 0o700)
	result, err := WriteSnapshot(context.Background(), cfg, []Shard{
		{Table: "new", Path: "data/new/items.age", Rows: []row{{ID: "2"}}},
	}, first)
	if err == nil || !strings.Contains(err.Error(), "committed") || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("missing explicit post-commit cleanup outcome: %v", err)
	}
	actual, readErr := ReadManifest(cfg.Repo)
	if readErr != nil || result.Format != FormatVersion || !EquivalentManifest(result, actual) {
		t.Fatalf("missing committed manifest: result=%+v actual=%+v err=%v", result, actual, readErr)
	}
	if _, err := ReadSnapshot(cfg, result); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotWriterHonorsHeldOSLock(t *testing.T) {
	cfg := publicationConfig(t)
	if err := os.MkdirAll(cfg.Repo, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := filelock.Acquire(filepath.Join(cfg.Repo, ".crawlkit-backup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := WriteSnapshot(context.Background(), cfg, nil, Manifest{}); err == nil {
		t.Fatal("writer ignored held lock with empty metadata")
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("contending writer touched manifest: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSnapshot(context.Background(), cfg, nil, Manifest{}); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalEntryAcceptsLegacyAndGeneratedPaths(t *testing.T) {
	rel := "data/messages/01.jsonl.gz.age"
	hash := SHA256Hex([]byte("fixture"))
	legacy := versionedShardPath(rel, hash)
	generated := strings.TrimSuffix(legacy, ".age") + "-" + strings.Repeat("a", 32) + ".age"
	for _, physical := range []string{rel, legacy, generated} {
		entry := ShardEntry{Table: "messages", Path: physical, SHA256: hash}
		manifest := Manifest{Shards: []ShardEntry{entry}}
		if got, ok := manifest.logicalEntry("messages", rel); !ok || got != entry {
			t.Fatalf("logical lookup failed for %q", physical)
		}
		if _, ok := manifest.logicalEntry("messages", "data/messages/02.jsonl.gz.age"); ok {
			t.Fatalf("different logical shard matched %q", physical)
		}
	}
	for _, suffix := range []string{strings.Repeat("A", 32), strings.Repeat("g", 32), "a", strings.Repeat("a", 32) + "/other"} {
		manifest := Manifest{Shards: []ShardEntry{{Table: "messages", Path: strings.TrimSuffix(legacy, ".age") + "-" + suffix + ".age", SHA256: hash}}}
		if _, ok := manifest.logicalEntry("messages", rel); ok {
			t.Fatalf("invalid generated suffix accepted: %q", suffix)
		}
	}
}

func TestWriteNewShardDoesNotClobberExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.age")
	before := []byte("published ciphertext")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNewShard(context.Background(), path, []byte("replacement")); err == nil {
		t.Fatal("existing path accepted")
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("existing ciphertext changed: %q, %v", after, err)
	}
}
