package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SidecarTreeOptions struct {
	SourceDir string
	RootDir   string
	TargetDir string
	Kind      string
	Include   func(relativePath string) bool
	Prune     func(relativePath string) bool
}

// SyncSidecarTree atomically copies a managed directory tree into a snapshot,
// fingerprints every regular file, and removes stale files under TargetDir.
func SyncSidecarTree(ctx context.Context, opts SidecarTreeOptions) ([]Sidecar, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source := strings.TrimSpace(opts.SourceDir)
	root := strings.TrimSpace(opts.RootDir)
	if source == "" || root == "" {
		return nil, errors.New("sidecar source and snapshot root are required")
	}
	targetRel, err := cleanRelativeDir(opts.TargetDir)
	if err != nil {
		return nil, err
	}
	targetRoot := filepath.Join(root, filepath.FromSlash(targetRel))
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create sidecar target: %w", err)
	}
	sourceRoot, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, fmt.Errorf("resolve sidecar source: %w", err)
	}
	targetRoot, err = filepath.EvalSymlinks(targetRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve sidecar target: %w", err)
	}
	if pathsOverlap(sourceRoot, targetRoot) {
		return nil, fmt.Errorf("sidecar source and target trees overlap: %s and %s", sourceRoot, targetRoot)
	}
	keep := map[string]struct{}{}
	var sidecars []Sidecar
	err = filepath.WalkDir(source, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sourcePath == source || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("sidecar source symlink is not allowed: %s", sourcePath)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("sidecar source is not a regular file: %s", sourcePath)
		}
		rel, err := filepath.Rel(source, sourcePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if opts.Include != nil && !opts.Include(rel) {
			return nil
		}
		destination := filepath.Join(targetRoot, filepath.FromSlash(rel))
		size, hash, err := copyFingerprintFile(sourcePath, destination)
		if err != nil {
			return err
		}
		manifestPath := filepath.ToSlash(filepath.Join(targetRel, rel))
		keep[filepath.Clean(destination)] = struct{}{}
		sidecars = append(sidecars, Sidecar{Name: rel, Path: manifestPath, Kind: opts.Kind, Size: size, SHA256: hash})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := pruneSidecarTree(ctx, targetRoot, keep, opts.Prune); err != nil {
		return nil, err
	}
	sort.Slice(sidecars, func(i, j int) bool { return sidecars[i].Path < sidecars[j].Path })
	return sidecars, nil
}

func cleanRelativeDir(value string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("sidecar target must be a relative directory: %q", value)
	}
	return clean, nil
}

func copyFingerprintFile(source, target string) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, "", err
	}
	in, err := os.Open(source) // #nosec G304 -- source is selected by the caller and validated as a regular file.
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return 0, "", err
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
		return 0, "", err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hash), in)
	if err != nil {
		_ = tmp.Close()
		return 0, "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return 0, "", err
	}
	committed = true
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func pruneSidecarTree(ctx context.Context, root string, keep map[string]struct{}, shouldPrune func(relativePath string) bool) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := keep[filepath.Clean(path)]; ok {
			return nil
		}
		if shouldPrune != nil {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !shouldPrune(filepath.ToSlash(rel)) {
				return nil
			}
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale sidecar %s: %w", path, err)
		}
		return nil
	})
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
