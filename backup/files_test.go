package backup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEncryptedSnapshotFilesDeduplicateAndRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
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
	for _, shard := range []Shard{
		{Table: fileIndexTable, Path: "data/custom.jsonl.gz.age", Rows: []row{}},
		{Table: "custom", Path: fileIndexPath, Rows: []row{}},
		{Table: "custom", Path: "data/files/aa/custom.gz.age", Rows: []row{}},
		{Table: "custom", Path: `data\files\index.jsonl.gz.age`, Rows: []row{}},
		{Table: "custom", Path: `data/files\index.jsonl.gz.age`, Rows: []row{}},
	} {
		if _, err := WriteSnapshotWithFiles(ctx, Config{}, []Shard{shard}, nil, Manifest{}); err == nil {
			t.Fatalf("reserved shard should fail: %#v", shard)
		}
	}
}
