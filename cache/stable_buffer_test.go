package cache

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyStableFilesVaryingSizes(t *testing.T) {
	sourceDir := t.TempDir()
	var sources []string
	contents := [][]byte{
		bytes.Repeat([]byte("large"), (2<<20)/5+17),
		[]byte("small"),
		nil,
		bytes.Repeat([]byte("last"), 1024),
	}
	for i, data := range contents {
		path := filepath.Join(sourceDir, fmt.Sprintf("source-%d", i))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, path)
	}
	_, copies, cleanup, err := CopyStableFiles(t.Context(), sources, StableOptions{TempDir: t.TempDir()})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if len(copies) != len(contents) {
		t.Fatalf("copied %d files, want %d", len(copies), len(contents))
	}
	for i, path := range copies {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, contents[i]) {
			t.Fatalf("copy %d differs: got %d bytes, want %d", i, len(got), len(contents[i]))
		}
	}
}

func BenchmarkCopyStableFiles(b *testing.B) {
	for _, count := range []int{1, 32, 128} {
		b.Run(fmt.Sprintf("files=%d", count), func(b *testing.B) {
			sourceDir, tempDir := b.TempDir(), b.TempDir()
			sources := make([]string, count)
			data := bytes.Repeat([]byte("x"), 4096)
			for i := range sources {
				sources[i] = filepath.Join(sourceDir, fmt.Sprintf("source-%d", i))
				if err := os.WriteFile(sources[i], data, 0o600); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.SetBytes(int64(count * len(data)))
			b.ResetTimer()
			for b.Loop() {
				_, _, cleanup, err := CopyStableFiles(context.Background(), sources, StableOptions{TempDir: tempDir})
				cleanup()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
