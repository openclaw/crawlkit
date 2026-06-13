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
