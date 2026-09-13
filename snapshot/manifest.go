package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Sidecar struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Kind   string `json:"kind,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type Manifest struct {
	Version     int               `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	Tables      []TableManifest   `json:"tables"`
	Sidecars    []Sidecar         `json:"sidecars,omitempty"`
	Files       map[string]string `json:"files,omitempty"`
}

type TableManifest struct {
	Name          string         `json:"name"`
	File          string         `json:"file,omitempty"`
	Files         []string       `json:"files"`
	FileManifests []FileManifest `json:"file_manifests,omitempty"`
	Columns       []string       `json:"columns"`
	Rows          int            `json:"rows"`
}

type FileManifest struct {
	Path   string `json:"path"`
	Rows   int    `json:"rows"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

var ErrNoManifest = errors.New("pack manifest not found")

func ReadManifest(rootDir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(rootDir, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrNoManifest
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return manifest, nil
}

func WriteManifest(rootDir string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return fmt.Errorf("create root dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ManifestName), data, 0o600); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func confinedSnapshotFile(rootDir, rel string) (string, error) {
	local := filepath.FromSlash(rel)
	// Keep literal filesystem names; this checks traversal, not symlink targets.
	if !filepath.IsLocal(local) || filepath.Clean(local) == "." || strings.ContainsRune(local, 0) {
		return "", fmt.Errorf("snapshot file path %q is not under the snapshot root", rel)
	}
	return filepath.Join(rootDir, local), nil
}

func tableShardDir(table string) (string, error) {
	table = strings.TrimSpace(table)
	if table == "" {
		return "", fmt.Errorf("table name is required")
	}
	if filepath.IsAbs(table) || strings.ContainsAny(table, `/\`) || table == "." || table == ".." || strings.Contains(table, "\x00") {
		return "", fmt.Errorf("table name %q is not safe for snapshot paths", table)
	}
	return filepath.ToSlash(filepath.Join("tables", table)), nil
}

func tableFileManifests(table TableManifest) []FileManifest {
	if len(table.FileManifests) > 0 {
		out := make([]FileManifest, len(table.FileManifests))
		copy(out, table.FileManifests)
		return out
	}
	files := table.Files
	if len(files) == 0 && strings.TrimSpace(table.File) != "" {
		files = []string{table.File}
	}
	out := make([]FileManifest, 0, len(files))
	for _, file := range files {
		out = append(out, FileManifest{Path: file})
	}
	return out
}

func importFileManifests(table TableManifest) ([]FileManifest, error) {
	if table.Rows < 0 {
		return nil, fmt.Errorf("table %s: negative row count", table.Name)
	}
	entries := tableFileManifests(table)
	modern := len(table.FileManifests) > 0
	remainingRows := table.Rows
	byPath := make(map[string]FileManifest, len(entries))
	for _, file := range entries {
		// Use the same lexical path identity as opening the shard.
		key, err := confinedSnapshotFile(".", file.Path)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", table.Name, err)
		}
		if _, exists := byPath[key]; exists {
			return nil, fmt.Errorf("table %s: duplicate file manifest path %q", table.Name, file.Path)
		}
		byPath[key] = file
		if modern {
			if file.Rows < 0 {
				return nil, fmt.Errorf("table %s: negative row count for %q", table.Name, file.Path)
			}
			// Subtract from the declared total to avoid overflowing a sum of rows.
			if file.Rows > remainingRows {
				return nil, fmt.Errorf("table %s: row count does not match file manifests", table.Name)
			}
			remainingRows -= file.Rows
		}
	}
	if !modern {
		// Legacy table totals remain advisory, including positive values.
		return entries, nil
	}
	if remainingRows != 0 {
		return nil, fmt.Errorf("table %s: row count does not match file manifests", table.Name)
	}
	paths := table.Files
	if table.File != "" {
		key, err := confinedSnapshotFile(".", table.File)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", table.Name, err)
		}
		if len(paths) == 0 {
			paths = []string{table.File}
		} else if len(paths) != 1 {
			return nil, fmt.Errorf("table %s: inconsistent file paths", table.Name)
		} else if listedKey, err := confinedSnapshotFile(".", paths[0]); err != nil || listedKey != key {
			return nil, fmt.Errorf("table %s: inconsistent file paths", table.Name)
		}
	}
	if len(paths) == 0 {
		paths = fileManifestPaths(table.FileManifests)
	}
	files := make([]FileManifest, 0, len(paths))
	for _, path := range paths {
		key, err := confinedSnapshotFile(".", path)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", table.Name, err)
		}
		file, ok := byPath[key]
		if !ok {
			return nil, fmt.Errorf("table %s: missing file manifest for %q", table.Name, path)
		}
		file.Path = path
		files = append(files, file)
		delete(byPath, key)
	}
	if len(byPath) != 0 {
		return nil, fmt.Errorf("table %s: file manifests do not match file paths", table.Name)
	}
	return files, nil
}
