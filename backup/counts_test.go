package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEquivalentManifestDistinguishesZeroCountKeys(t *testing.T) {
	left := Manifest{Counts: map[string]int{"old": 0}}
	right := Manifest{Counts: map[string]int{"new": 0}}
	if EquivalentManifest(left, right) {
		t.Fatal("different zero-valued counters compare equal")
	}
	if !EquivalentManifest(Manifest{}, Manifest{Counts: map[string]int{}}) {
		t.Fatal("nil and empty counters should remain equivalent")
	}
}

func TestWriteSnapshotPublishesChangedEmptyCountKey(t *testing.T) {
	root := t.TempDir()
	identity := filepath.Join(root, "identity")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(root, "pack"), Identity: identity, Recipients: []string{recipient}}
	shard := Shard{Table: "items", CountKey: "old", Path: "data/items.jsonl.gz.age", Rows: []row{}}
	first, err := WriteSnapshot(t.Context(), cfg, []Shard{shard}, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	shard.CountKey = "new"
	second, err := WriteSnapshot(t.Context(), cfg, []Shard{shard}, first)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ReadManifest(cfg.Repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range []Manifest{second, stored} {
		count, present := manifest.Counts["new"]
		if _, old := manifest.Counts["old"]; old || !present || count != 0 {
			t.Fatalf("counts = %#v", manifest.Counts)
		}
		if manifest.Shards[0].Path != first.Shards[0].Path {
			t.Fatal("unchanged shard was not reused")
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, first.Shards[0].Path)); err != nil {
		t.Fatal(err)
	}
}
