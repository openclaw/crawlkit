package cache

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSnapshotFilePreservesLiteralNames(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	now := func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) }
	names := []string{"archive.db", " archive.db", "日本語.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "archive.db ", "  ")
	}
	snapshots := make(map[string]string)
	for _, name := range names {
		if err := os.WriteFile(source, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		snap, err := SnapshotFile(SnapshotOptions{SourcePath: source, CacheDir: filepath.Join(dir, "cache"), Name: name, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		if want := now().Format("20060102T150405Z") + "-" + name; filepath.Base(snap.Path) != want {
			t.Errorf("snapshot filename = %q, want %q", filepath.Base(snap.Path), want)
		}
		snapshots[name] = snap.Path
	}
	for name, path := range snapshots {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != name {
			t.Errorf("snapshot %q overwritten: %q", name, body)
		}
	}
}

func TestSnapshotFileRejectsNonBasenames(t *testing.T) {
	for _, name := range []string{".", "..", filepath.Join("nested", "archive.db"), filepath.Join("..", "archive.db"), filepath.Join("..", "..", "..", "escape.db")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.db")
			if err := os.WriteFile(source, []byte("captured"), 0o600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(dir, "escape.db")
			if err := os.WriteFile(outside, []byte("preserved"), 0o600); err != nil {
				t.Fatal(err)
			}
			cacheDir := filepath.Join(dir, "cache")
			_, err := SnapshotFile(SnapshotOptions{SourcePath: source, CacheDir: cacheDir, Name: name})
			if err == nil || !strings.Contains(err.Error(), "snapshot name must be a file name") {
				t.Errorf("expected name validation error, got %v", err)
			}
			body, err := os.ReadFile(outside)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "preserved" {
				t.Errorf("outside file overwritten: %q", body)
			}
			entries, err := os.ReadDir(cacheDir)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("invalid name left cache files: %v", entries)
			}
		})
	}
}
