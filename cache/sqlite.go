package cache

import (
	"errors"
	"fmt"
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
// or retained. The caller must exclude concurrent destination readers/writers
// during replacement; publishing three fixed filenames is not atomic.
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
	stage, err := os.MkdirTemp(destination, ".sqlite-snapshot-")
	if err != nil {
		return SQLiteSnapshot{}, fmt.Errorf("create sqlite snapshot staging dir: %w", err)
	}
	retainStage := false
	defer func() {
		if !retainStage {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, dir := range []string{"next", "previous"} {
		if err := os.Mkdir(filepath.Join(stage, dir), 0o700); err != nil {
			return SQLiteSnapshot{}, err
		}
	}
	stagedPath := filepath.Join(stage, "next", name)

	result := SQLiteSnapshot{SourcePath: source, Path: filepath.Join(destination, name)}
	copiedSuffixes := map[string]bool{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		size, copied, err := copyOptionalFile(source+suffix, stagedPath+suffix, opts.MaxFileBytes)
		if err != nil {
			return SQLiteSnapshot{}, err
		}
		if suffix == "" && !copied {
			return SQLiteSnapshot{}, errors.New("sqlite source disappeared during capture")
		}
		if copied {
			copiedSuffixes[suffix] = true
			result.Files = append(result.Files, result.Path+suffix)
			result.SizeBytes += size
		}
	}
	retainStage, err = publishSQLiteBundle(stagedPath, filepath.Join(stage, "previous", name), result.Path, copiedSuffixes)
	if err != nil {
		return SQLiteSnapshot{}, err
	}
	if err := os.RemoveAll(stage); err != nil {
		retainStage = true
		return result, fmt.Errorf("sqlite snapshot committed; staging cleanup failed: %w", err)
	}
	return result, nil
}

func publishSQLiteBundle(staged, previous, target string, copied map[string]bool) (bool, error) {
	suffixes := []string{"", "-wal", "-shm"}
	var old, published []string
	for _, suffix := range suffixes {
		info, err := os.Lstat(target + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return false, fmt.Errorf("sqlite snapshot destination is not a file: %s", target+suffix)
		}
		old = append(old, suffix)
	}
	var moved []string
	rollback := func(cause error) (bool, error) {
		var restoreErr error
		for _, suffix := range published {
			if err := os.Remove(target + suffix); err != nil {
				restoreErr = errors.Join(restoreErr, err)
			}
		}
		for _, suffix := range moved {
			if err := os.Rename(previous+suffix, target+suffix); err != nil {
				restoreErr = errors.Join(restoreErr, err)
			}
		}
		if restoreErr != nil {
			return true, errors.Join(cause, fmt.Errorf("restore previous sqlite snapshot; recovery files retained at %s: %w", filepath.Dir(previous), restoreErr))
		}
		return false, cause
	}
	for _, suffix := range old {
		if err := os.Rename(target+suffix, previous+suffix); err != nil {
			return rollback(err)
		}
		moved = append(moved, suffix)
	}
	// Install the new sidecars before making their corresponding main visible.
	for _, suffix := range []string{"-wal", "-shm", ""} {
		if !copied[suffix] {
			continue
		}
		if err := os.Rename(staged+suffix, target+suffix); err != nil {
			return rollback(err)
		}
		published = append(published, suffix)
	}
	return false, nil
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
	copied, exceeded, err := copyLimited(tmp, in, maxBytes)
	if err == nil && exceeded {
		err = fmt.Errorf("sqlite snapshot file %s exceeds limit %d", source, maxBytes)
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
