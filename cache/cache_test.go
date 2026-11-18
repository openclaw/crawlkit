package cache

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSnapshotFileCopiesReadOnly(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotFile(SnapshotOptions{
		SourcePath:   source,
		CacheDir:     filepath.Join(dir, "cache"),
		MaxFileBytes: 4,
		Now:          func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.SizeBytes != 4 {
		t.Fatalf("size = %d", snap.SizeBytes)
	}
	data, err := os.ReadFile(snap.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Fatalf("snapshot data = %q", string(data))
	}
	info, err := os.Stat(snap.Path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		got := info.Mode().Perm()
		t.Fatalf("mode = %o", got)
	}
}

func TestSnapshotFileSizeGuard(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SnapshotFile(SnapshotOptions{SourcePath: source, CacheDir: filepath.Join(dir, "cache"), MaxFileBytes: 1})
	if err == nil {
		t.Fatal("expected size guard error")
	}
}

func TestSnapshotSQLiteCopiesBundle(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	for suffix, body := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		if err := os.WriteFile(source+suffix, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(dir, "snapshot")
	snap, err := SnapshotSQLite(SQLiteSnapshotOptions{
		SourcePath:     source,
		DestinationDir: destination,
		Name:           "archive.sqlite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Path != filepath.Join(destination, "archive.sqlite") || len(snap.Files) != 3 || snap.SizeBytes != 8 {
		t.Fatalf("snapshot = %+v", snap)
	}
	for suffix, want := range map[string]string{"": "db", "-wal": "wal", "-shm": "shm"} {
		body, err := os.ReadFile(snap.Path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want {
			t.Fatalf("%s = %q", suffix, body)
		}
	}
}

func TestSnapshotSQLiteValidation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	if err := os.WriteFile(source, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []SQLiteSnapshotOptions{
		{},
		{SourcePath: source},
		{SourcePath: source, DestinationDir: filepath.Join(dir, "out"), Name: "../escape.db"},
		{SourcePath: source, DestinationDir: filepath.Join(dir, "out"), MaxFileBytes: 1},
	} {
		if _, err := SnapshotSQLite(opts); err == nil {
			t.Fatalf("expected validation error for %+v", opts)
		}
	}
}

func TestSnapshotFilePreservesExistingCapture(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	opts := SnapshotOptions{SourcePath: source, CacheDir: filepath.Join(dir, "cache"), Now: func() time.Time {
		return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	}}
	if err := os.WriteFile(source, []byte("first capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := SnapshotFile(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("later capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotFile(opts); !errors.Is(err, os.ErrExist) {
		t.Errorf("colliding capture error = %v, want already exists", err)
	}
	data, err := os.ReadFile(first.Path)
	if err != nil || string(data) != "first capture" {
		t.Fatalf("first capture changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(opts.CacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("collision left extra cache files: %v, %v", entries, err)
	}
}

func TestSnapshotFileConcurrentCollision(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	if err := os.WriteFile(source, []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := SnapshotOptions{SourcePath: source, CacheDir: filepath.Join(dir, "cache"), Now: func() time.Time {
		return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	}}
	type result struct {
		snapshot Snapshot
		err      error
	}
	const writers = 4
	results := make(chan result, writers)
	start := make(chan struct{})
	for range writers {
		go func() {
			<-start
			snapshot, err := SnapshotFile(opts)
			results <- result{snapshot, err}
		}()
	}
	close(start)
	winners := 0
	var path string
	for range writers {
		r := <-results
		if r.err == nil {
			winners++
			path = r.snapshot.Path
		} else if !errors.Is(r.err, os.ErrExist) {
			t.Errorf("unexpected capture error: %v", r.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful publishers = %d, want 1", winners)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "capture" {
		t.Fatalf("published capture = %q, %v", data, err)
	}
	entries, err := os.ReadDir(opts.CacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("publishers left extra cache files: %v, %v", entries, err)
	}
}
