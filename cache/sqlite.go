package cache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SQLiteSnapshotOptions struct {
	SourcePath     string
	DestinationDir string
	Name           string
	MaxFileBytes   int64
}

type SQLiteSnapshot struct {
	SourcePath string   `json:"source_path"`
	Path       string   `json:"path"`
	Files      []string `json:"files"`
	SizeBytes  int64    `json:"size_bytes"`
}

// SnapshotSQLite copies a SQLite database and its optional WAL/SHM sidecars.
// The caller owns DestinationDir and decides whether snapshots are temporary
// or retained.
func SnapshotSQLite(opts SQLiteSnapshotOptions) (SQLiteSnapshot, error) {
	source := strings.TrimSpace(opts.SourcePath)
	if source == "" {
		return SQLiteSnapshot{}, errors.New("sqlite source path is required")
	}
	destination := strings.TrimSpace(opts.DestinationDir)
	if destination == "" {
		return SQLiteSnapshot{}, errors.New("sqlite destination dir is required")
	}
	info, err := os.Stat(source)
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("stat sqlite source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return SQLiteSnapshot{}, fmt.Errorf("sqlite source is not a regular file: %s", source)
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = filepath.Base(source)
	}
	if filepath.Base(name) != name || name == "." || name == ".." {
		return SQLiteSnapshot{}, fmt.Errorf("sqlite snapshot name must be a file name: %q", name)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("create sqlite snapshot dir: %w", err)
	}

	result := SQLiteSnapshot{SourcePath: source, Path: filepath.Join(destination, name)}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		size, copied, err := copyOptionalFile(source+suffix, result.Path+suffix, opts.MaxFileBytes)
		if err != nil {
			return SQLiteSnapshot{}, err
		}
		if copied {
			result.Files = append(result.Files, result.Path+suffix)
			result.SizeBytes += size
		}
	}
	return result, nil
}

func SQLiteModifiedAfter(path string, cutoff time.Time) bool {
	path = strings.TrimSpace(path)
	if path == "" || cutoff.IsZero() {
		return false
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

func copyOptionalFile(source, target string, maxBytes int64) (int64, bool, error) {
	in, err := os.Open(source) // #nosec G304 -- caller explicitly selects the local SQLite source.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("open sqlite snapshot source: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, false, fmt.Errorf("stat sqlite snapshot source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, false, fmt.Errorf("sqlite snapshot source is not a regular file: %s", source)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return 0, false, fmt.Errorf("sqlite snapshot file %s is %d bytes, exceeds limit %d", source, info.Size(), maxBytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return 0, false, fmt.Errorf("create sqlite snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return 0, false, err
	}
	var copied int64
	if maxBytes > 0 {
		limited := &io.LimitedReader{R: in, N: maxBytes + 1}
		copied, err = io.Copy(tmp, limited)
		if err == nil && copied > maxBytes {
			err = fmt.Errorf("sqlite snapshot file %s exceeds limit %d", source, maxBytes)
		}
	} else {
		copied, err = io.Copy(tmp, in)
	}
	if err != nil {
		_ = tmp.Close()
		return 0, false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, false, err
	}
	if err := tmp.Close(); err != nil {
		return 0, false, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return 0, false, fmt.Errorf("commit sqlite snapshot: %w", err)
	}
	committed = true
	return copied, true, nil
}
