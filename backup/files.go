package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

type File struct {
	Path   string
	Source string
}

type encryptedFileLoader func(FileEntry) (io.ReadCloser, error)

func CollectFiles(ctx context.Context, root, prefix string) ([]File, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil, fmt.Errorf("file root is required")
	}
	prefix = strings.Trim(strings.TrimSpace(filepath.ToSlash(prefix)), "/")
	if prefix != "" {
		if _, err := cleanFilePath(prefix); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var files []File
	err := filepath.WalkDir(root, func(source string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, source)
		if err != nil {
			return err
		}
		logical := filepath.ToSlash(rel)
		if prefix != "" {
			logical = path.Join(prefix, logical)
		}
		logical, err = cleanFilePath(logical)
		if err != nil {
			return err
		}
		files = append(files, File{Path: logical, Source: source})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func writeFiles(ctx context.Context, cfg Config, old Manifest, files []File, reuseEncrypted bool) ([]FileEntry, error) {
	oldByHash := make(map[string]FileEntry, len(old.Files))
	if reuseEncrypted {
		for _, entry := range old.Files {
			oldByHash[entry.SHA256] = entry
		}
	}
	written := make(map[string]FileEntry, len(files))
	seenPaths := make(map[string]struct{}, len(files))
	out := make([]FileEntry, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		logical, err := cleanFilePath(file.Path)
		if err != nil {
			return nil, err
		}
		if _, exists := seenPaths[logical]; exists {
			return nil, fmt.Errorf("duplicate backup file path: %s", logical)
		}
		seenPaths[logical] = struct{}{}
		info, err := os.Lstat(file.Source)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("backup file is not regular: %s", file.Source)
		}
		tmpPath, hashValue, size, encryptedSize, err := encryptFileTemp(ctx, file.Source, cfg.Repo, cfg.Recipients)
		if err != nil {
			return nil, err
		}
		keepTemp := true
		defer func() {
			if keepTemp {
				_ = os.Remove(tmpPath)
			}
		}()
		if entry, ok := written[hashValue]; ok {
			if err := os.Remove(tmpPath); err != nil {
				return nil, err
			}
			keepTemp = false
			entry.Path = logical
			out = append(out, entry)
			continue
		}
		shard := path.Join("data/files", hashValue[:2], hashValue+".gz.age")
		if oldEntry, ok := oldByHash[hashValue]; ok && oldEntry.Shard == shard {
			target, resolveErr := ResolveShardPath(cfg.Repo, shard)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if encryptedInfo, statErr := os.Stat(target); statErr == nil {
				if err := os.Remove(tmpPath); err != nil {
					return nil, err
				}
				keepTemp = false
				entry := FileEntry{Path: logical, Shard: shard, SHA256: hashValue, Size: size, Bytes: encryptedInfo.Size()}
				written[hashValue] = entry
				out = append(out, entry)
				continue
			}
		}
		target, err := ResolveShardPath(cfg.Repo, shard)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := os.Rename(tmpPath, target); err != nil {
			return nil, err
		}
		keepTemp = false
		if err := syncDir(filepath.Dir(target)); err != nil {
			return nil, err
		}
		entry := FileEntry{Path: logical, Shard: shard, SHA256: hashValue, Size: size, Bytes: encryptedSize}
		written[hashValue] = entry
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func RestoreFiles(ctx context.Context, cfg Config, manifest Manifest, targetRoot string) (int, error) {
	return restoreFilesWith(ctx, cfg.Identity, manifest, targetRoot, func(entry FileEntry) (io.ReadCloser, error) {
		shard, err := ResolveShardPath(cfg.Repo, entry.Shard)
		if err != nil {
			return nil, err
		}
		return os.Open(shard) // #nosec G304 -- ResolveShardPath confines manifest-controlled paths below data/.
	})
}

func restoreFilesWith(ctx context.Context, identityPath string, manifest Manifest, targetRoot string, load encryptedFileLoader) (int, error) {
	if manifest.Format != FormatVersion {
		return 0, fmt.Errorf("unsupported backup format %d", manifest.Format)
	}
	identityData, err := os.ReadFile(expandHome(identityPath)) // #nosec G304 -- path is configured by the caller.
	if err != nil {
		return 0, err
	}
	identity, err := parseIdentity(identityData)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		logical, err := cleanFilePath(entry.Path)
		if err != nil {
			return 0, err
		}
		if _, exists := seen[logical]; exists {
			return 0, fmt.Errorf("duplicate backup file path: %s", logical)
		}
		seen[logical] = struct{}{}
		if _, err := ResolveShardPath(".", entry.Shard); err != nil {
			return 0, err
		}
		ciphertext, err := load(entry)
		if err != nil {
			return 0, err
		}
		err = restoreFile(ctx, identity, ciphertext, targetRoot, logical, entry)
		closeErr := ciphertext.Close()
		if err != nil {
			return 0, err
		}
		if closeErr != nil {
			return 0, closeErr
		}
	}
	return len(manifest.Files), nil
}

func restoreFile(ctx context.Context, identity *age.X25519Identity, ciphertext io.Reader, targetRoot, logical string, entry FileEntry) error {
	target, err := safeRestoreTarget(targetRoot, logical)
	if err != nil {
		return err
	}
	decrypted, err := age.Decrypt(ciphertext, identity)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(decrypted)
	if err != nil {
		return err
	}
	defer gz.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	hasher := sha256.New()
	size, err := copyContext(ctx, io.MultiWriter(tmp, hasher), gz)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if size != entry.Size || hex.EncodeToString(hasher.Sum(nil)) != entry.SHA256 {
		_ = tmp.Close()
		return fmt.Errorf("backup file verification failed for %s", logical)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

func encryptFileTemp(ctx context.Context, source, repo string, recipientStrings []string) (string, string, int64, int64, error) {
	recipients, err := parseRecipients(recipientStrings)
	if err != nil {
		return "", "", 0, 0, err
	}
	tmpDir := filepath.Join(repo, "data", "files")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", "", 0, 0, err
	}
	in, err := os.Open(source) // #nosec G304 -- source is explicitly supplied by the caller.
	if err != nil {
		return "", "", 0, 0, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(tmpDir, ".file.tmp-")
	if err != nil {
		return "", "", 0, 0, err
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
		return "", "", 0, 0, err
	}
	encrypted, err := age.Encrypt(tmp, recipients...)
	if err != nil {
		_ = tmp.Close()
		return "", "", 0, 0, err
	}
	gz := gzip.NewWriter(encrypted)
	gz.ModTime = time.Unix(0, 0).UTC()
	hasher := sha256.New()
	size, err := copyContext(ctx, gz, io.TeeReader(in, hasher))
	if err != nil {
		_ = gz.Close()
		_ = encrypted.Close()
		_ = tmp.Close()
		return "", "", 0, 0, err
	}
	if err := gz.Close(); err != nil {
		_ = encrypted.Close()
		_ = tmp.Close()
		return "", "", 0, 0, err
	}
	if err := encrypted.Close(); err != nil {
		_ = tmp.Close()
		return "", "", 0, 0, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", "", 0, 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", "", 0, 0, err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return "", "", 0, 0, err
	}
	cleanup = false
	return tmpPath, hex.EncodeToString(hasher.Sum(nil)), size, info.Size(), nil
}

func safeRestoreTarget(root, logical string) (string, error) {
	logical, err := cleanFilePath(logical)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return "", fmt.Errorf("restore root is required")
	}
	if err := ensureDirectory(root); err != nil {
		return "", err
	}
	parts := strings.Split(logical, "/")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		if err := ensureDirectory(parent); err != nil {
			return "", err
		}
	}
	target := filepath.Join(root, filepath.FromSlash(logical))
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("restore target is not a regular file: %s", logical)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return target, nil
}

func ensureDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return os.Mkdir(dir, 0o700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("restore path is not a directory: %s", dir)
	}
	return nil
}

func cleanFilePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\\") {
		return "", fmt.Errorf("invalid backup file path: %s", value)
	}
	clean := path.Clean(value)
	local := filepath.FromSlash(clean)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || filepath.IsAbs(local) || filepath.VolumeName(local) != "" {
		return "", fmt.Errorf("backup file path escapes restore root: %s", value)
	}
	return clean, nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 256*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			written, writeErr := dst.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func readEncryptedFileBytes(data []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(data))
}
