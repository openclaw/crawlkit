package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncSidecarTreeRootAliasesAndInvalidRoots(t *testing.T) {
	source := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "page.txt"), []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := SidecarTreeOptions{SourceDir: source, RootDir: root, TargetDir: "pages"}
	if _, err := SyncSidecarTree(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	opts.SourceDir = alias
	sidecars, err := SyncSidecarTree(context.Background(), opts)
	if err != nil || len(sidecars) != 1 {
		t.Fatalf("root symlink: sidecars=%v, error=%v", sidecars, err)
	}
	for _, invalid := range []string{filepath.Join(source, "page.txt"), filepath.Join(source, "missing")} {
		opts.SourceDir = invalid
		if _, err := SyncSidecarTree(context.Background(), opts); err == nil {
			t.Errorf("invalid source %q accepted", invalid)
		}
		got, err := os.ReadFile(filepath.Join(root, "pages", "page.txt"))
		if err != nil || string(got) != "retained" {
			t.Fatalf("previous sidecar lost: %q, error=%v", got, err)
		}
	}
}

func TestSyncSidecarTreeInvalidRootDoesNotCreateTarget(t *testing.T) {
	root := t.TempDir()
	_, err := SyncSidecarTree(context.Background(), SidecarTreeOptions{
		SourceDir: filepath.Join(root, "missing"),
		RootDir:   root, TargetDir: "pages",
	})
	if err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "pages")); !os.IsNotExist(err) {
		t.Fatalf("invalid source created target: %v", err)
	}
}
