package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
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
	info   os.FileInfo
}

const (
	fileIndexTable = "_backup_files"
	fileIndexPath  = "data/files/index.jsonl.gz.age"
)

type fileIndexRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type indexedFile struct {
	Entry  FileEntry
	Record fileIndexRecord
}

type encryptedFileLoader func(string) (io.ReadCloser, error)

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
		files = append(files, File{Path: logical, Source: source, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func writeFiles(ctx context.Context, cfg Config, old Manifest, files []File, reuseEncrypted bool) ([]FileEntry, []fileIndexRecord, error) {
	ordered := append([]File(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	oldByHash := make(map[string]indexedFile, len(old.Files))
	if reuseEncrypted && len(files) > 0 {
		records, err := loadLocalFileIndex(cfg, old)
		if err == nil {
			for index, entry := range old.Files {
				record := records[index]
				oldByHash[record.SHA256] = indexedFile{Entry: entry, Record: record}
			}
		}
	}
	written := make(map[string]FileEntry, len(files))
	seenPaths := make(map[string]struct{}, len(files))
	out := make([]FileEntry, 0, len(files))
	index := make([]fileIndexRecord, 0, len(files))
	for _, file := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		logical, err := cleanFilePath(file.Path)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := seenPaths[logical]; exists {
			return nil, nil, fmt.Errorf("duplicate backup file path: %s", logical)
		}
		seenPaths[logical] = struct{}{}
		hashValue, size, sourceInfo, err := hashFile(ctx, file)
		if err != nil {
			return nil, nil, err
		}
		if entry, ok, err := reusableFileEntry(cfg, oldByHash, written, hashValue, size); err != nil {
			return nil, nil, err
		} else if ok {
			out = append(out, entry)
			index = append(index, fileIndexRecord{Path: logical, SHA256: hashValue, Size: size})
			continue
		}
		tmpPath, hashValue, size, encryptedSize, err := encryptFileTemp(ctx, file, sourceInfo, cfg.Repo, cfg.Recipients)
		if err != nil {
			return nil, nil, err
		}
		keepTemp := true
		defer func() {
			if keepTemp {
				_ = os.Remove(tmpPath)
			}
		}()
		if entry, ok, err := reusableFileEntry(cfg, oldByHash, written, hashValue, size); err != nil {
			return nil, nil, err
		} else if ok {
			if err := os.Remove(tmpPath); err != nil {
				return nil, nil, err
			}
			keepTemp = false
			out = append(out, entry)
			index = append(index, fileIndexRecord{Path: logical, SHA256: hashValue, Size: size})
			continue
		}
		shard, err := randomFileShard(cfg.Repo)
		if err != nil {
			return nil, nil, err
		}
		target, err := ResolveShardPath(cfg.Repo, shard)
		if err != nil {
			return nil, nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, nil, err
		}
		if err := os.Rename(tmpPath, target); err != nil {
			return nil, nil, err
		}
		keepTemp = false
		if err := syncDir(filepath.Dir(target)); err != nil {
			return nil, nil, err
		}
		entry := FileEntry{Shard: shard, Bytes: encryptedSize}
		written[hashValue] = entry
		out = append(out, entry)
		index = append(index, fileIndexRecord{Path: logical, SHA256: hashValue, Size: size})
	}
	return out, index, nil
}

func reusableFileEntry(cfg Config, oldByHash map[string]indexedFile, written map[string]FileEntry, hashValue string, size int64) (FileEntry, bool, error) {
	if entry, ok := written[hashValue]; ok {
		return entry, true, nil
	}
	oldFile, ok := oldByHash[hashValue]
	if !ok || oldFile.Record.Size != size {
		return FileEntry{}, false, nil
	}
	target, err := ResolveShardPath(cfg.Repo, oldFile.Entry.Shard)
	if err != nil {
		return FileEntry{}, false, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return FileEntry{}, false, nil
		}
		return FileEntry{}, false, err
	}
	entry := FileEntry{Shard: oldFile.Entry.Shard, Bytes: info.Size()}
	written[hashValue] = entry
	return entry, true, nil
}

func loadLocalFileIndex(cfg Config, manifest Manifest) ([]fileIndexRecord, error) {
	if len(manifest.Files) == 0 {
		return nil, nil
	}
	identityData, err := os.ReadFile(expandHome(cfg.Identity)) // #nosec G304 -- path is configured by the caller.
	if err != nil {
		return nil, err
	}
	identity, err := parseIdentity(identityData)
	if err != nil {
		return nil, err
	}
	return readFileIndex(identity, manifest, func(rel string) (io.ReadCloser, error) {
		shard, err := ResolveShardPath(cfg.Repo, rel)
		if err != nil {
			return nil, err
		}
		return os.Open(shard) // #nosec G304 -- ResolveShardPath confines manifest-controlled paths below data/.
	})
}

func randomFileShard(repo string) (string, error) {
	for range 10 {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", err
		}
		rel := path.Join("data/files/objects", hex.EncodeToString(id[:])+".gz.age")
		target, err := ResolveShardPath(repo, rel)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(target); os.IsNotExist(err) {
			return rel, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("generate unique backup file object path")
}

func RestoreFiles(ctx context.Context, cfg Config, manifest Manifest, targetRoot string) (int, error) {
	return RestoreFilesUnder(ctx, cfg, manifest, targetRoot, "")
}

func RestoreFilesUnder(ctx context.Context, cfg Config, manifest Manifest, targetRoot, requiredPrefix string) (int, error) {
	return restoreFilesWith(ctx, cfg.Identity, manifest, targetRoot, requiredPrefix, func(rel string) (io.ReadCloser, error) {
		shard, err := ResolveShardPath(cfg.Repo, rel)
		if err != nil {
			return nil, err
		}
		return os.Open(shard) // #nosec G304 -- ResolveShardPath confines manifest-controlled paths below data/.
	})
}

func restoreFilesWith(ctx context.Context, identityPath string, manifest Manifest, targetRoot, requiredPrefix string, load encryptedFileLoader) (int, error) {
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
	records, err := readFileIndex(identity, manifest, load)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	prefix := ""
	if requiredPrefix != "" {
		prefix, err = cleanFilePath(requiredPrefix)
		if err != nil {
			return 0, err
		}
	}
	for index, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		record := records[index]
		logical := record.Path
		logical, err = cleanFilePath(logical)
		if err != nil {
			return 0, err
		}
		if prefix != "" && logical != prefix && !strings.HasPrefix(logical, prefix+"/") {
			return 0, fmt.Errorf("backup file path is outside required prefix %s: %s", prefix, logical)
		}
		if _, exists := seen[logical]; exists {
			return 0, fmt.Errorf("duplicate backup file path: %s", logical)
		}
		seen[logical] = struct{}{}
		if _, err := ResolveShardPath(".", entry.Shard); err != nil {
			return 0, err
		}
		ciphertext, err := load(entry.Shard)
		if err != nil {
			return 0, err
		}
		err = restoreFile(ctx, identity, ciphertext, targetRoot, logical, record)
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

func readFileIndex(identity *age.X25519Identity, manifest Manifest, load encryptedFileLoader) ([]fileIndexRecord, error) {
	if len(manifest.Files) == 0 {
		return []fileIndexRecord{}, nil
	}
	var indexEntry *ShardEntry
	for index := range manifest.Shards {
		if manifest.Shards[index].Table != fileIndexTable {
			continue
		}
		if indexEntry != nil {
			return nil, fmt.Errorf("backup contains multiple file indexes")
		}
		indexEntry = &manifest.Shards[index]
	}
	if indexEntry == nil {
		return nil, fmt.Errorf("backup file index is missing")
	}
	ciphertext, err := load(indexEntry.Path)
	if err != nil {
		return nil, err
	}
	plaintext, decryptErr := decryptFilePayload(identity, ciphertext)
	closeErr := ciphertext.Close()
	if decryptErr != nil {
		return nil, decryptErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if SHA256Hex(plaintext) != indexEntry.SHA256 {
		return nil, fmt.Errorf("backup file index hash mismatch")
	}
	var records []fileIndexRecord
	if err := DecodeJSONL(plaintext, &records); err != nil {
		return nil, fmt.Errorf("decode backup file index: %w", err)
	}
	if len(records) != len(manifest.Files) {
		return nil, fmt.Errorf("backup file index count mismatch")
	}
	seen := make(map[string]struct{}, len(records))
	for index := range records {
		record := &records[index]
		logical, err := cleanFilePath(record.Path)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[logical]; exists {
			return nil, fmt.Errorf("duplicate backup file index path: %s", logical)
		}
		seen[logical] = struct{}{}
		record.Path = logical
		if record.Size < 0 {
			return nil, fmt.Errorf("invalid backup file size for %s", logical)
		}
		hashBytes, err := hex.DecodeString(record.SHA256)
		if err != nil || len(hashBytes) != sha256.Size {
			return nil, fmt.Errorf("invalid backup file hash for %s", logical)
		}
	}
	return records, nil
}

func decryptFilePayload(identity *age.X25519Identity, ciphertext io.Reader) ([]byte, error) {
	decrypted, err := age.Decrypt(ciphertext, identity)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(decrypted)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}

func restoreFile(ctx context.Context, identity *age.X25519Identity, ciphertext io.Reader, targetRoot, logical string, record fileIndexRecord) error {
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
	if size != record.Size || hex.EncodeToString(hasher.Sum(nil)) != record.SHA256 {
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
	// os.Rename replaces regular files, including via MoveFileEx with
	// MOVEFILE_REPLACE_EXISTING on Windows, without first hiding the old file.
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

func encryptFileTemp(ctx context.Context, source File, expected os.FileInfo, repo string, recipientStrings []string) (string, string, int64, int64, error) {
	recipients, err := parseRecipients(recipientStrings)
	if err != nil {
		return "", "", 0, 0, err
	}
	tmpDir := filepath.Join(repo, "data", "files")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", "", 0, 0, err
	}
	in, _, err := openSourceFile(source, expected)
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

func hashFile(ctx context.Context, source File) (string, int64, os.FileInfo, error) {
	file, info, err := openSourceFile(source, source.info)
	if err != nil {
		return "", 0, nil, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := copyContext(ctx, hasher, file)
	if err != nil {
		return "", 0, nil, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, info, nil
}

func openSourceFile(source File, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(source.Source)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("backup file is not regular: %s", source.Source)
	}
	if expected != nil && !os.SameFile(expected, before) {
		return nil, nil, fmt.Errorf("backup file changed during collection: %s", source.Source)
	}
	file, err := os.Open(source.Source) // #nosec G304 -- identity checks bind the opened file to the collected regular file before reads.
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || (expected != nil && !os.SameFile(expected, after)) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("backup file changed before open: %s", source.Source)
	}
	return file, after, nil
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
