package cache

import (
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
		SourcePath: source,
		CacheDir:   filepath.Join(dir, "cache"),
		Now:        func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
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

func TestSnapshotFileAllowsExactCopyLimit(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	if err := os.WriteFile(source, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotFile(SnapshotOptions{
		SourcePath:   source,
		CacheDir:     filepath.Join(dir, "cache"),
		MaxFileBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.SizeBytes != 4 {
		t.Fatalf("size = %d", snap.SizeBytes)
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
	cutoff := time.Now().Add(-time.Hour)
	if !SQLiteModifiedAfter(source, cutoff) {
		t.Fatal("sqlite bundle should be newer than cutoff")
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
	if SQLiteModifiedAfter("", time.Now()) || SQLiteModifiedAfter(source, time.Time{}) {
		t.Fatal("empty sqlite modification checks should be false")
	}
}
