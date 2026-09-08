package snapshot

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/internal/filelock"
)

func exportSnapshot(ctx context.Context, opts ExportOptions) (result Manifest, resultErr error) {
	if opts.DB == nil && opts.ReadTx == nil {
		return Manifest{}, errors.New("db is required")
	}
	if strings.TrimSpace(opts.RootDir) == "" {
		return Manifest{}, errors.New("root dir is required")
	}
	if len(opts.Tables) == 0 {
		return Manifest{}, errors.New("at least one table is required")
	}
	for _, table := range opts.Tables {
		if _, err := tableShardDir(table); err != nil {
			return Manifest{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(opts.RootDir, 0o755); err != nil {
		return Manifest{}, err
	}
	lock, err := filelock.Acquire(filepath.Join(opts.RootDir, ".crawlkit-snapshot.lock"))
	if err != nil {
		return Manifest{}, fmt.Errorf("lock snapshot writer: %w", err)
	}
	defer lock.Close()
	root, err := os.OpenRoot(opts.RootDir)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	previous, err := ReadManifest(opts.RootDir)
	if err != nil && !errors.Is(err, ErrNoManifest) {
		return Manifest{}, err
	}
	if err := root.MkdirAll(filepath.FromSlash("tables/.generations"), 0o755); err != nil {
		return Manifest{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Manifest{}, err
	}
	generation := "tables/.generations/" + hex.EncodeToString(id[:])
	if err := root.Mkdir(filepath.FromSlash(generation), 0o755); err != nil {
		return Manifest{}, err
	}
	created := []string{}
	committed := false
	defer func() {
		keep := managedSnapshotFiles(result)
		for _, rel := range created {
			if committed && keep[rel] {
				continue
			}
			if err := root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
				label := "remove unpublished snapshot shard"
				if committed {
					label = "snapshot manifest committed; cleanup failed"
				}
				resultErr = errors.Join(resultErr, fmt.Errorf("%s: %w", label, err))
			}
		}
	}()
	tx := opts.ReadTx
	if tx == nil {
		tx, err = opts.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return Manifest{}, fmt.Errorf("begin export read transaction: %w", err)
		}
		defer tx.Rollback()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	maxShardBytes := opts.MaxShardBytes
	if maxShardBytes == 0 {
		maxShardBytes = defaultMaxShardBytes
	}
	manifest := Manifest{
		Version: 1, GeneratedAt: now().UTC(), Sidecars: opts.Sidecars,
		Files: map[string]string{"manifest": ManifestName},
	}
	for _, table := range opts.Tables {
		entry, err := exportTable(ctx, tx, root, generation, table, maxShardBytes, opts.Filter, opts.FilterTx,
			func(rel string) { created = append(created, rel) })
		if err != nil {
			return Manifest{}, err
		}
		if err := reuseSnapshotShards(root, previous, &entry); err != nil {
			return Manifest{}, err
		}
		manifest.Tables = append(manifest.Tables, entry)
	}
	if opts.ReadTx == nil {
		if err := tx.Commit(); err != nil {
			return Manifest{}, fmt.Errorf("finish export read transaction: %w", err)
		}
	}
	if err := publishSnapshotManifest(ctx, root, manifest, hex.EncodeToString(id[:])); err != nil {
		return Manifest{}, err
	}
	committed = true
	result = manifest
	keep := managedSnapshotFiles(manifest)
	for rel := range managedSnapshotFiles(previous) {
		if keep[rel] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("snapshot manifest committed; cleanup failed: %w", err)
		}
		if err := root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("snapshot manifest committed; cleanup failed: %w", err)
		}
	}
	return result, nil
}

func publishSnapshotManifest(ctx context.Context, root *os.Root, manifest Manifest, generation string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := ".manifest-" + generation + ".tmp"
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// No error is reported after rename: callers may only roll back before it.
	return root.Rename(tmp, ManifestName)
}

var snapshotOrdinal = regexp.MustCompile(`^[0-9]{6,}\.jsonl\.gz$`)
var snapshotGeneration = regexp.MustCompile(`^[0-9a-f]{32}$`)

func logicalShardPath(table, physical string) (string, bool) {
	if _, err := tableShardDir(table); err != nil {
		return physical, false
	}
	parts := strings.Split(physical, "/")
	if len(parts) == 3 && parts[0] == "tables" && parts[1] == table && snapshotOrdinal.MatchString(parts[2]) {
		return physical, true
	}
	if len(parts) == 5 && parts[0] == "tables" && parts[1] == ".generations" &&
		snapshotGeneration.MatchString(parts[2]) && parts[3] == table && snapshotOrdinal.MatchString(parts[4]) {
		return "tables/" + table + "/" + parts[4], true
	}
	return physical, false
}

func logicalFileKey(table, physical string) string {
	logical, _ := logicalShardPath(table, physical)
	return logical
}

func sameLogicalFileManifest(table string, a, b FileManifest) bool {
	a.Path, b.Path = logicalFileKey(table, a.Path), logicalFileKey(table, b.Path)
	return sameFileManifest(a, b)
}

func sameLogicalFileManifests(table string, a, b []FileManifest) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameLogicalFileManifest(table, a[i], b[i]) {
			return false
		}
	}
	return true
}

func managedSnapshotFiles(manifest Manifest) map[string]bool {
	files := map[string]bool{}
	for _, table := range manifest.Tables {
		paths := table.Files
		if len(paths) == 0 && table.File != "" {
			paths = []string{table.File}
		}
		for _, rel := range paths {
			if _, managed := logicalShardPath(table.Name, rel); managed {
				files[rel] = true
			}
		}
	}
	return files
}

func reuseSnapshotShards(root *os.Root, previous Manifest, current *TableManifest) error {
	owned := managedSnapshotFiles(previous)
	prior := map[string]FileManifest{}
	for _, table := range previous.Tables {
		if table.Name != current.Name {
			continue
		}
		for _, file := range tableFileManifests(table) {
			if owned[file.Path] {
				prior[logicalFileKey(table.Name, file.Path)] = file
			}
		}
	}
	for i, file := range current.FileManifests {
		old, ok := prior[logicalFileKey(current.Name, file.Path)]
		if !ok || !sameLogicalFileManifest(current.Name, old, file) {
			continue
		}
		same, err := sameSnapshotBytes(root, old.Path, file.Path)
		if err != nil {
			return err
		}
		if same {
			current.Files[i] = old.Path
			current.FileManifests[i].Path = old.Path
		}
	}
	return nil
}

func sameSnapshotBytes(root *os.Root, oldPath, newPath string) (bool, error) {
	info, err := root.Stat(filepath.FromSlash(oldPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	old, err := root.Open(filepath.FromSlash(oldPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer old.Close()
	next, err := root.Open(filepath.FromSlash(newPath))
	if err != nil {
		return false, err
	}
	defer next.Close()
	a, b := make([]byte, 64*1024), make([]byte, 64*1024)
	for {
		n, errA := io.ReadFull(old, a)
		m, errB := io.ReadFull(next, b)
		if errA != nil && errA != io.EOF && errA != io.ErrUnexpectedEOF {
			return false, errA
		}
		if errB != nil && errB != io.EOF && errB != io.ErrUnexpectedEOF {
			return false, errB
		}
		if n != m || !bytes.Equal(a[:n], b[:m]) {
			return false, nil
		}
		if errA != nil || errB != nil {
			return errA == errB, nil
		}
	}
}
