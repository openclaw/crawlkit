package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const FormatVersion = 1

type Config struct {
	Repo       string
	Identity   string
	Recipients []string
}

type Manifest struct {
	Format     int            `json:"format"`
	Encrypted  bool           `json:"encrypted"`
	Exported   time.Time      `json:"exported"`
	Recipients []string       `json:"recipients,omitempty"`
	Counts     map[string]int `json:"counts"`
	Shards     []ShardEntry   `json:"shards"`
}

type Shard struct {
	Table string
	Path  string
	Rows  any
}

type ShardEntry struct {
	Table  string `json:"table"`
	Path   string `json:"path"`
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type DecodedShard struct {
	Entry     ShardEntry
	Plaintext []byte
}

func WriteSnapshot(ctx context.Context, cfg Config, shards []Shard, old Manifest) (Manifest, error) {
	_ = ctx
	recipients := normalizedStrings(cfg.Recipients)
	reuseEncrypted := sameStrings(old.Recipients, recipients)
	manifest := Manifest{
		Format:     FormatVersion,
		Encrypted:  true,
		Exported:   time.Now().UTC(),
		Recipients: recipients,
		Counts:     map[string]int{},
	}
	for _, shard := range shards {
		plaintext, rows, err := EncodeJSONL(shard.Rows)
		if err != nil {
			return Manifest{}, fmt.Errorf("encode %s: %w", shard.Table, err)
		}
		entry, err := WriteShard(cfg, old, shard.Table, shard.Path, plaintext, rows, reuseEncrypted)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Counts[shard.Table] += rows
		manifest.Shards = append(manifest.Shards, entry)
	}
	sort.Slice(manifest.Shards, func(i, j int) bool { return manifest.Shards[i].Path < manifest.Shards[j].Path })
	if EquivalentManifest(old, manifest) {
		return old, nil
	}
	if err := RemoveStaleShards(cfg.Repo, manifest.Shards); err != nil {
		return Manifest{}, err
	}
	if err := WriteManifest(cfg.Repo, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ReadSnapshot(cfg Config, manifest Manifest) ([]DecodedShard, error) {
	if manifest.Format != FormatVersion {
		return nil, fmt.Errorf("unsupported backup format %d", manifest.Format)
	}
	var out []DecodedShard
	for _, shard := range manifest.Shards {
		plaintext, err := DecryptShardFile(cfg, shard)
		if err != nil {
			return nil, err
		}
		if got := SHA256Hex(plaintext); got != shard.SHA256 {
			return nil, fmt.Errorf("backup shard hash mismatch for %s", shard.Path)
		}
		out = append(out, DecodedShard{Entry: shard, Plaintext: plaintext})
	}
	return out, nil
}

func WriteShard(cfg Config, old Manifest, table, rel string, plaintext []byte, rows int, reuseEncrypted bool) (ShardEntry, error) {
	hash := SHA256Hex(plaintext)
	target, err := ResolveShardPath(cfg.Repo, rel)
	if err != nil {
		return ShardEntry{}, err
	}
	if oldEntry, ok := old.Entry(rel); reuseEncrypted && ok && oldEntry.SHA256 == hash {
		if info, err := os.Stat(target); err == nil {
			oldEntry.Bytes = info.Size()
			return oldEntry, nil
		}
	}
	encrypted, _, err := EncryptShard(plaintext, cfg.Recipients)
	if err != nil {
		return ShardEntry{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return ShardEntry{}, err
	}
	if err := os.WriteFile(target, encrypted, 0o600); err != nil {
		return ShardEntry{}, err
	}
	return ShardEntry{Table: table, Path: rel, Rows: rows, SHA256: hash, Bytes: int64(len(encrypted))}, nil
}

func DecryptShardFile(cfg Config, shard ShardEntry) ([]byte, error) {
	target, err := ResolveShardPath(cfg.Repo, shard.Path)
	if err != nil {
		return nil, err
	}
	ciphertext, err := os.ReadFile(target) // #nosec G304 -- ResolveShardPath confines manifest-controlled paths below data/.
	if err != nil {
		return nil, err
	}
	return DecryptShard(ciphertext, cfg.Identity)
}

func ResolveShardPath(repo, rel string) (string, error) {
	clean := path.Clean(strings.TrimSpace(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("backup shard path escapes backup root: %s", rel)
	}
	if !strings.HasPrefix(clean, "data/") || !strings.HasSuffix(clean, ".age") {
		return "", fmt.Errorf("invalid backup shard path: %s", rel)
	}
	full := filepath.Join(repo, filepath.FromSlash(clean))
	root := filepath.Clean(filepath.Join(repo, "data"))
	parent := filepath.Clean(filepath.Dir(full))
	if parent != root && !strings.HasPrefix(parent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("backup shard path escapes backup root: %s", rel)
	}
	return full, nil
}

func EncodeJSONL(rows any) ([]byte, int, error) {
	value := reflect.ValueOf(rows)
	if value.Kind() != reflect.Slice {
		return nil, 0, fmt.Errorf("unsupported JSONL rows %T", rows)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := 0; i < value.Len(); i++ {
		if err := enc.Encode(value.Index(i).Interface()); err != nil {
			return nil, 0, err
		}
	}
	return buf.Bytes(), value.Len(), nil
}

func DecodeJSONL[T any](plaintext []byte, out *[]T) error {
	scanner := bufio.NewScanner(bytes.NewReader(plaintext))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var value T
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return err
		}
		*out = append(*out, value)
	}
	return scanner.Err()
}

func ReadManifest(repo string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(repo, "manifest.json")) // #nosec G304 -- repo is configured by caller.
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func WriteManifest(repo string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(repo, "manifest.json"), data, 0o600)
}

func (m Manifest) Entry(path string) (ShardEntry, bool) {
	for _, shard := range m.Shards {
		if shard.Path == path {
			return shard, true
		}
	}
	return ShardEntry{}, false
}

func EquivalentManifest(a, b Manifest) bool {
	if a.Format != b.Format || a.Encrypted != b.Encrypted || !sameStrings(a.Recipients, b.Recipients) || !sameCounts(a.Counts, b.Counts) || len(a.Shards) != len(b.Shards) {
		return false
	}
	for i := range a.Shards {
		left, right := a.Shards[i], b.Shards[i]
		left.Bytes, right.Bytes = 0, 0
		if left != right {
			return false
		}
	}
	return true
}

func RemoveStaleShards(repo string, shards []ShardEntry) error {
	keep := map[string]struct{}{}
	for _, shard := range shards {
		keep[filepath.Clean(filepath.Join(repo, filepath.FromSlash(shard.Path)))] = struct{}{}
	}
	root := filepath.Join(repo, "data")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	var stale []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".age") {
			return nil
		}
		clean := filepath.Clean(path)
		if _, ok := keep[clean]; ok {
			return nil
		}
		stale = append(stale, clean)
		return nil
	}); err != nil {
		return err
	}
	for _, path := range stale {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("stale shard path escapes backup root: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func EncryptShard(plaintext []byte, recipients []string) ([]byte, string, error) {
	return encryptShard(plaintext, recipients)
}

func DecryptShard(ciphertext []byte, identityPath string) ([]byte, error) {
	return decryptShard(ciphertext, identityPath)
}

func SHA256Hex(data []byte) string {
	return sha256Hex(data)
}

func normalizedStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	a, b = normalizedStrings(a), normalizedStrings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, left := range a {
		if b[key] != left {
			return false
		}
	}
	return true
}

func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if after, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, after)
		}
	}
	return p
}
