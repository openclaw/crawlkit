package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSyncSidecarTreeCaseOnlyRenameKeepsCopiedFile(t *testing.T) {
	probeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(probeDir, "CaseProbe"), []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(probeDir, "caseprobe")); err != nil {
		t.Skip("filesystem does not fold case")
	}

	source := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "Media"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Media", "x.txt"), []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTxt := filepath.Join(root, "pages", "media", "x.txt")
	oldMd := filepath.Join(root, "pages", "media", "old.md")
	if err := os.MkdirAll(filepath.Dir(oldTxt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldTxt, []byte("old bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldMd, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	sidecars, err := SyncSidecarTree(context.Background(), SidecarTreeOptions{
		SourceDir: source,
		RootDir:   root,
		TargetDir: "pages",
		Include: func(rel string) bool {
			return strings.HasSuffix(rel, ".txt") || strings.HasSuffix(rel, ".md")
		},
		Prune: func(rel string) bool {
			return strings.HasSuffix(rel, ".txt") || strings.HasSuffix(rel, ".md")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sidecars) != 1 {
		t.Fatalf("sidecars = %+v", sidecars)
	}
	manifestPath := filepath.Join(root, filepath.FromSlash(sidecars[0].Path))
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("stat manifest path %s: %v", sidecars[0].Path, err)
	}
	got, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new bytes" {
		t.Fatalf("manifest file bytes = %q, want new bytes", got)
	}
	if _, err := os.Stat(oldMd); !os.IsNotExist(err) {
		t.Fatalf("old.md still present: %v", err)
	}
}

func TestSidecarOnDiskRelUsesListingCase(t *testing.T) {
	folded := fstest.MapFS{
		"media/x.txt": &fstest.MapFile{Data: []byte("x"), Mode: 0o644},
	}
	got, err := sidecarOnDiskRel(folded, "Media/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "media/x.txt" {
		t.Fatalf("keep key = %q, want media/x.txt", got)
	}

	exact := fstest.MapFS{
		"MEDIA/x.txt": &fstest.MapFile{Data: []byte("a"), Mode: 0o644},
		"media/x.txt": &fstest.MapFile{Data: []byte("b"), Mode: 0o644},
		"Media/x.txt": &fstest.MapFile{Data: []byte("c"), Mode: 0o644},
	}
	got, err = sidecarOnDiskRel(exact, "Media/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Media/x.txt" {
		t.Fatalf("exact directory keep key = %q, want Media/x.txt", got)
	}

	missing := fstest.MapFS{}
	got, err = sidecarOnDiskRel(missing, "Media/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Media/x.txt" {
		t.Fatalf("missing component keep key = %q, want Media/x.txt", got)
	}
}
