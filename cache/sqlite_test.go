package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestSnapshotSQLiteReusedDestinationDropsStaleWAL(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open(ctx, store.Options{Path: source, Schema: `create table items(value text); insert into items values('old');`})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	destination := t.TempDir()
	sentinel := filepath.Join(destination, "unrelated")
	if err := os.WriteFile(sentinel, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := SQLiteSnapshotOptions{SourcePath: source, DestinationDir: destination, Name: "archive.db"}
	first, err := SnapshotSQLite(opts)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(first.Path + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("fixture lacks uncheckpointed WAL: %v, %v", wal, err)
	}
	if _, err := st.DB().ExecContext(ctx, `update items set value='new'; pragma wal_checkpoint(TRUNCATE);`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("source WAL still exists: %v", err)
	}
	second, err := SnapshotSQLite(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(second.Path + suffix); !os.IsNotExist(err) {
			t.Errorf("stale destination sidecar %s retained: %v", suffix, err)
		}
	}
	ro, err := store.OpenReadOnly(ctx, second.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var value string
	if err := ro.DB().QueryRowContext(ctx, `select value from items`).Scan(&value); err != nil || value != "new" {
		t.Fatalf("reused snapshot value = %q, error = %v", value, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "retain" {
		t.Fatalf("unrelated destination file changed: %q, %v", data, err)
	}
}

func TestSnapshotSQLiteFailurePreservesPreviousBundle(t *testing.T) {
	for _, failingSuffix := range []string{"-wal", "-shm"} {
		t.Run(failingSuffix, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source.db")
			destination := t.TempDir()
			target := filepath.Join(destination, "archive.db")
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.WriteFile(target+suffix, []byte("old"+suffix), 0o600); err != nil {
					t.Fatal(err)
				}
				data := []byte("new")
				if suffix == failingSuffix {
					data = []byte("exceeds maximum")
				}
				if err := os.WriteFile(source+suffix, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SnapshotSQLite(SQLiteSnapshotOptions{
				SourcePath: source, DestinationDir: destination, Name: "archive.db", MaxFileBytes: 8,
			}); err == nil {
				t.Fatal("expected sidecar size failure")
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				got, err := os.ReadFile(target + suffix)
				if err != nil || string(got) != "old"+suffix {
					t.Errorf("previous %s changed: %q, %v", suffix, got, err)
				}
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 3 {
				t.Fatalf("staging files leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestPublishSQLiteBundleRollsBackFailedMainPromotion(t *testing.T) {
	target := filepath.Join(t.TempDir(), "archive.db")
	staged := filepath.Join(t.TempDir(), "archive.db")
	previous := filepath.Join(t.TempDir(), "archive.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(target+suffix, []byte("old"+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(staged+"-wal", []byte("new WAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	retained, err := publishSQLiteBundle(staged, previous, target, map[string]bool{"": true, "-wal": true})
	if err == nil || retained {
		t.Fatalf("failed main promotion: retained=%v, error=%v", retained, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(target + suffix)
		if err != nil || string(data) != "old"+suffix {
			t.Fatalf("old bundle not restored at %s: %q, %v", suffix, data, err)
		}
	}
}

func TestSnapshotSQLitePreservesExistingAliasCompatibility(t *testing.T) {
	for _, kind := range []string{"same path", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.db")
			if err := os.WriteFile(source, []byte("source bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			name := "source.db"
			if kind == "hardlink" {
				name = "alias.db"
				if err := os.Link(source, filepath.Join(dir, name)); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			}
			snapshot, err := SnapshotSQLite(SQLiteSnapshotOptions{SourcePath: source, DestinationDir: dir, Name: name})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(source)
			if err != nil || string(data) != "source bytes" {
				t.Fatalf("source changed: %q, %v", data, err)
			}
			data, err = os.ReadFile(snapshot.Path)
			if err != nil || string(data) != "source bytes" {
				t.Fatalf("snapshot content: %q, %v", data, err)
			}
			if kind == "hardlink" {
				after, err := os.Stat(source)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("distinct source inode replaced: %v", err)
				}
			}
		})
	}
}
