package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestRestoreFileRejectsOversizedPayloadBeforeReadingItAll(t *testing.T) {
	ctx := context.Background()
	cfg := publicationConfig(t)
	key, err := os.ReadFile(cfg.Identity)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := parseIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	ciphertext, _, err := encryptShardContext(ctx, payload, cfg.Recipients)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(ciphertext)
	root := t.TempDir()
	target := filepath.Join(root, "file.bin")
	if err := os.WriteFile(target, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := fileIndexRecord{Path: "file.bin", Size: 1, SHA256: SHA256Hex(payload)}
	if err := restoreFile(ctx, identity, reader, root, record.Path, record); err == nil {
		t.Fatal("oversized payload was accepted")
	}
	if consumed := len(ciphertext) - reader.Len(); consumed > len(ciphertext)/2 {
		t.Errorf("read %d of %d encrypted bytes despite a one-byte declared size", consumed, len(ciphertext))
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "previous" {
		t.Fatalf("previous target changed: %q, %v", got, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
		t.Fatalf("failed restore leaked temporary files: %v, %v", entries, err)
	}
}

func TestRestoreFileChecksExactSizeAndAuthenticatedEOF(t *testing.T) {
	ctx := context.Background()
	cfg := publicationConfig(t)
	key, err := os.ReadFile(cfg.Identity)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := parseIdentity(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, payload string
		size          int64
		corrupt       bool
		wantError     bool
	}{
		{name: "empty"},
		{name: "exact", payload: "content", size: 7},
		{name: "short", payload: "content", size: 8, wantError: true},
		{name: "long", payload: "content", size: 6, wantError: true},
		{name: "damaged tail", payload: "content", size: 7, corrupt: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ciphertext, _, err := encryptShardContext(ctx, []byte(tc.payload), cfg.Recipients)
			if err != nil {
				t.Fatal(err)
			}
			if tc.corrupt {
				ciphertext[len(ciphertext)-1] ^= 1
			}
			root := t.TempDir()
			record := fileIndexRecord{Path: "file.bin", Size: tc.size, SHA256: SHA256Hex([]byte(tc.payload))}
			err = restoreFile(ctx, identity, bytes.NewReader(ciphertext), root, record.Path, record)
			if (err != nil) != tc.wantError {
				t.Fatalf("restore error = %v, want error = %v", err, tc.wantError)
			}
			got, readErr := os.ReadFile(filepath.Join(root, record.Path))
			if tc.wantError {
				if !os.IsNotExist(readErr) {
					t.Fatalf("invalid payload was published: %q, %v", got, readErr)
				}
			} else if readErr != nil || string(got) != tc.payload {
				t.Fatalf("restored content = %q, %v", got, readErr)
			}
		})
	}
}

func TestEncryptedSnapshotFilesDeduplicateAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"photo.jpg", filepath.Join("nested", "copy.jpg")} {
			if err := os.WriteFile(filepath.Join(source, name), []byte("neighbor media"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		source += " "
	}
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"photo.jpg", filepath.Join("nested", "copy.jpg")} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("same private media"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(source, "photo.jpg"), filepath.Join(source, "linked.jpg")); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	files, err := CollectFiles(ctx, source, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("collected files = %#v", files)
	}
	if files[0].Source != filepath.Join(source, "nested", "copy.jpg") || files[1].Source != filepath.Join(source, "photo.jpg") {
		t.Fatalf("collected files lost literal root: %#v", files)
	}
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	if err := os.MkdirAll(cfg.Repo, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := WriteSnapshotWithFiles(ctx, cfg, nil, files, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 2 || manifest.Files[0].Shard != manifest.Files[1].Shard {
		t.Fatalf("files were not content-deduplicated: %#v", manifest.Files)
	}
	manifestBody, err := os.ReadFile(filepath.Join(cfg.Repo, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifestBody), "photo.jpg") || strings.Contains(string(manifestBody), "copy.jpg") {
		t.Fatalf("manifest exposes logical media paths: %s", manifestBody)
	}
	if strings.Contains(string(manifestBody), SHA256Hex([]byte("same private media"))) {
		t.Fatalf("manifest exposes plaintext media hash: %s", manifestBody)
	}
	ciphertext, err := os.ReadFile(filepath.Join(cfg.Repo, filepath.FromSlash(manifest.Files[0].Shard)))
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == "same private media" {
		t.Fatal("backup file was stored as plaintext")
	}
	if err := RemoveStaleShards(cfg.Repo, manifest.Shards); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, filepath.FromSlash(manifest.Files[0].Shard))); err != nil {
		t.Fatalf("legacy stale cleanup removed file bundle object: %v", err)
	}
	second, err := WriteSnapshotWithFiles(ctx, cfg, nil, files, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !EquivalentManifest(manifest, second) || !second.Exported.Equal(manifest.Exported) {
		t.Fatalf("unchanged files rewrote manifest: %#v", second)
	}
	publicOnly := cfg
	publicOnly.Identity = ""
	publicOnly.Repo = filepath.Join(dir, "public-only")
	if err := os.MkdirAll(publicOnly.Repo, 0o700); err != nil {
		t.Fatal(err)
	}
	withoutReuse, err := WriteSnapshotWithFiles(ctx, publicOnly, nil, files, manifest)
	if err != nil {
		t.Fatalf("public-recipient-only writer should re-encrypt files: %v", err)
	}
	if len(withoutReuse.Files) != len(files) {
		t.Fatalf("public-only file manifest = %#v", withoutReuse.Files)
	}
	withoutFiles, err := WriteSnapshotWithFiles(ctx, publicOnly, nil, nil, withoutReuse)
	if err != nil {
		t.Fatalf("removing files should not require an identity: %v", err)
	}
	if len(withoutFiles.Files) != 0 {
		t.Fatalf("removed file manifest = %#v", withoutFiles.Files)
	}
	restoreRoot := filepath.Join(dir, "restore")
	neighborPhoto := filepath.Join(restoreRoot, "media", "photo.jpg")
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Dir(neighborPhoto), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(neighborPhoto, []byte("neighbor restore"), 0o600); err != nil {
			t.Fatal(err)
		}
		restoreRoot += " "
	}
	if err := os.MkdirAll(restoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	count, err := RestoreFiles(ctx, cfg, manifest, restoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("restored files = %d", count)
	}
	if runtime.GOOS != "windows" {
		if body, err := os.ReadFile(neighborPhoto); err != nil || string(body) != "neighbor restore" {
			t.Fatalf("restore changed neighboring root: %q, %v", body, err)
		}
	} else {
		for _, test := range []struct {
			name, source, target string
		}{
			{"root-space", source + " ", restoreRoot + " "},
			{"root-dot", source + ".", restoreRoot + "."},
			{"parent-space", filepath.Join(dir+" ", "source"), filepath.Join(dir+" ", "restore")},
			{"parent-dot", filepath.Join(dir+".", "source"), filepath.Join(dir+".", "restore")},
		} {
			t.Run(test.name, func(t *testing.T) {
				const diagnostic = "ambiguous trailing spaces or dots on Windows"
				if _, err := CollectFiles(ctx, test.source, "media"); err == nil || !strings.Contains(err.Error(), diagnostic) {
					t.Fatalf("ambiguous collection root error = %v", err)
				}
				if _, err := RestoreFiles(ctx, cfg, manifest, test.target); err == nil || !strings.Contains(err.Error(), diagnostic) {
					t.Fatalf("ambiguous restore root error = %v", err)
				}
				if body, err := os.ReadFile(neighborPhoto); err != nil || string(body) != "same private media" {
					t.Fatalf("rejected restore changed canonical file: %q, %v", body, err)
				}
			})
		}
	}
	for _, name := range []string{filepath.Join("media", "photo.jpg"), filepath.Join("media", "nested", "copy.jpg")} {
		body, err := os.ReadFile(filepath.Join(restoreRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "same private media" {
			t.Fatalf("restored %s = %q", name, body)
		}
	}
	photo := filepath.Join(restoreRoot, "media", "photo.jpg")
	if err := os.WriteFile(photo, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreFiles(ctx, cfg, manifest, restoreRoot); err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
	if body, err := os.ReadFile(photo); err != nil || string(body) != "same private media" {
		t.Fatalf("repeat restored photo = %q err=%v", body, err)
	}
	confinedRoot := filepath.Join(dir, "confined")
	if err := os.MkdirAll(confinedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreFilesUnder(ctx, cfg, manifest, confinedRoot, "attachments"); err == nil {
		t.Fatal("restore outside required prefix should fail")
	}
}

func TestRestoreFilesRejectsUnsafePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	if _, err := EnsureIdentity(identity); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: dir, Identity: identity}
	if _, err := safeRestoreTarget(filepath.Join(dir, "restore"), "../escape"); err == nil {
		t.Fatal("unsafe restore path should fail")
	}

	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := CollectFiles(ctx, source, "media")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := RecipientFromIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Recipients = []string{recipient}
	manifest, err := WriteSnapshotWithFiles(ctx, cfg, nil, files, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	restoreRoot := filepath.Join(dir, "target")
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(restoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(restoreRoot, "media"))
	if _, err := RestoreFiles(ctx, cfg, manifest, restoreRoot); err == nil {
		t.Fatal("symlinked restore directory should fail")
	}
}

func TestCollectFilesPreservesWhitespaceAndRejectsSwappedSymlink(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "source")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"name"}
	if runtime.GOOS != "windows" {
		names = append(names, "name ")
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := CollectFiles(ctx, root, "media")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(names) || files[0].Path != "media/name" || (runtime.GOOS != "windows" && files[1].Path != "media/name ") {
		t.Fatalf("whitespace paths changed: %#v", files)
	}

	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(files[0].Source); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, files[0].Source)
	identity := filepath.Join(dir, "age.key")
	recipient, err := EnsureIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Repo: filepath.Join(dir, "repo"), Identity: identity, Recipients: []string{recipient}}
	if _, err := WriteSnapshotWithFiles(ctx, cfg, nil, files, Manifest{}); err == nil {
		t.Fatal("symlink-swapped source should fail")
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("failed backup wrote a manifest: %v", err)
	}
}

func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestWriteSnapshotRejectsReservedFileIndexNamespace(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name      string
		shard     Shard
		wantError string
	}{
		{"reserved-table", Shard{Table: fileIndexTable, Path: "data/custom.jsonl.gz.age", Rows: []row{}}, "backup shard uses reserved file index namespace"},
		{"file-index", Shard{Table: "custom", Path: fileIndexPath, Rows: []row{}}, "backup shard uses reserved file index namespace"},
		{"file-object", Shard{Table: "custom", Path: "data/files/aa/custom.gz.age", Rows: []row{}}, "backup shard uses reserved file index namespace"},
		{"backslashes", Shard{Table: "custom", Path: `data\files\index.jsonl.gz.age`, Rows: []row{}}, "invalid backup shard path"},
		{"mixed-separators", Shard{Table: "custom", Path: `data/files\index.jsonl.gz.age`, Rows: []row{}}, "invalid backup shard path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WriteSnapshotWithFiles(ctx, Config{}, []Shard{tt.shard}, nil, Manifest{})
			if want := tt.wantError + ": " + tt.shard.Path; err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestRestoreValidatesAllFileMetadataBeforeWriting(t *testing.T) {
	ctx := context.Background()
	cfg := publicationConfig(t)
	source := t.TempDir()
	files := []File{
		{Path: "attachments/first.txt", Source: filepath.Join(source, "first")},
		{Path: "other/second.txt", Source: filepath.Join(source, "second")},
	}
	for _, file := range files {
		if err := os.WriteFile(file.Source, []byte("backup "+file.Path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := WriteSnapshotWithFiles(ctx, cfg, nil, files, Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("backup file count = %d", len(manifest.Files))
	}
	for _, tc := range []struct {
		name, prefix, diagnostic string
		badShard                 bool
	}{
		{name: "later prefix mismatch", prefix: "attachments", diagnostic: "outside required prefix"},
		{name: "later invalid shard", diagnostic: "escapes backup root", badShard: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := manifest
			current.Files = append([]FileEntry(nil), manifest.Files...)
			if tc.badShard {
				current.Files[1].Shard = "../escape.age"
			}
			target := t.TempDir()
			if err := os.Mkdir(filepath.Join(target, "attachments"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "attachments", "first.txt"), []byte("existing target"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := publishedFiles(t, target)
			count, err := RestoreFilesUnder(ctx, cfg, current, target, tc.prefix)
			if count != 0 || err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("restore count=%d err=%v, want metadata rejection", count, err)
			}
			if !reflect.DeepEqual(before, publishedFiles(t, target)) {
				t.Fatal("metadata rejection changed an earlier target file")
			}
		})
	}
}
