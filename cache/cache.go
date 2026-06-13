package cache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type SnapshotOptions struct {
	SourcePath   string
	CacheDir     string
	Name         string
	MaxFileBytes int64
	Now          func() time.Time
}

type Snapshot struct {
	SourcePath string    `json:"source_path"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

func SnapshotFile(opts SnapshotOptions) (Snapshot, error) {
	if opts.SourcePath == "" {
		return Snapshot{}, errors.New("source path is required")
	}
	if opts.CacheDir == "" {
		return Snapshot{}, errors.New("cache dir is required")
	}
	info, err := os.Stat(opts.SourcePath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("source is not a regular file: %s", opts.SourcePath)
	}
	if opts.MaxFileBytes > 0 && info.Size() > opts.MaxFileBytes {
		return Snapshot{}, fmt.Errorf("source file is %d bytes, exceeds limit %d", info.Size(), opts.MaxFileBytes)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("create cache dir: %w", err)
	}
	createdAt := now().UTC()
	name := opts.Name
	if name == "" {
		name = filepath.Base(opts.SourcePath)
	}
	target := filepath.Join(opts.CacheDir, createdAt.Format("20060102T150405Z")+"-"+name)
	src, err := os.Open(opts.SourcePath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open source: %w", err)
	}
	defer src.Close()
	tmp, err := os.CreateTemp(opts.CacheDir, "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return Snapshot{}, fmt.Errorf("create snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Snapshot{}, fmt.Errorf("chmod snapshot: %w", err)
	}
	var copied int64
	if opts.MaxFileBytes > 0 {
		limited := &io.LimitedReader{R: src, N: opts.MaxFileBytes + 1}
		copied, err = io.Copy(tmp, limited)
		if err == nil && copied > opts.MaxFileBytes {
			err = fmt.Errorf("source file exceeds limit %d", opts.MaxFileBytes)
		}
	} else {
		copied, err = io.Copy(tmp, src)
	}
	if err != nil {
		_ = tmp.Close()
		return Snapshot{}, fmt.Errorf("copy snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Snapshot{}, fmt.Errorf("close snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return Snapshot{}, fmt.Errorf("commit snapshot: %w", err)
	}
	cleanup = false
	return Snapshot{SourcePath: opts.SourcePath, Path: target, SizeBytes: copied, CreatedAt: createdAt}, nil
}
