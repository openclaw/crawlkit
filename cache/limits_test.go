package cache

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotsPreserveDataAtMaximumLimit(t *testing.T) {
	for _, kind := range []string{"file", "sqlite"} {
		t.Run(kind, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source.db")
			payload := []byte("synthetic snapshot payload")
			if err := os.WriteFile(source, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			var path string
			var size int64
			if kind == "file" {
				result, err := SnapshotFile(SnapshotOptions{SourcePath: source, CacheDir: t.TempDir(), MaxFileBytes: math.MaxInt64})
				if err != nil {
					t.Fatal(err)
				}
				path, size = result.Path, result.SizeBytes
			} else {
				result, err := SnapshotSQLite(SQLiteSnapshotOptions{SourcePath: source, DestinationDir: t.TempDir(), MaxFileBytes: math.MaxInt64})
				if err != nil {
					t.Fatal(err)
				}
				path, size = result.Path, result.SizeBytes
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != string(payload) || size != int64(len(payload)) {
				t.Fatalf("captured %q (%d bytes), want %q", data, size, payload)
			}
		})
	}
}

func TestCopyLimitedStopsAfterOverflowProbe(t *testing.T) {
	source := strings.NewReader("abcdef")
	var target bytes.Buffer
	copied, exceeded, err := copyLimited(&target, source, 3)
	if err != nil || !exceeded || copied != 3 || target.String() != "abc" || source.Len() != 2 {
		t.Fatalf("copy = %d, %t, %v, %q; unread = %d", copied, exceeded, err, target.String(), source.Len())
	}
}
