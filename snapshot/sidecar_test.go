package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestSyncSidecarTreeRejectsMixedRootOverlap(t *testing.T) {
	for _, relativeSource := range []bool{true, false} {
		t.Run(fmt.Sprintf("relative-source-%t", relativeSource), func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			source := filepath.Join(base, "source")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "page.txt"), []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			root := source
			if relativeSource {
				source = "source"
			} else {
				root = "source"
			}
			_, err := SyncSidecarTree(context.Background(), SidecarTreeOptions{SourceDir: source, RootDir: root, TargetDir: "pages"})
			if err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("overlapping trees accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(base, "source", "pages", "page.txt")); !os.IsNotExist(err) {
				t.Fatalf("source copied into itself: %v", err)
			}
		})
	}
}

func TestSyncSidecarTreeRejectsDestinationDirectorySymlinks(t *testing.T) {
	for _, external := range []bool{true, false} {
		t.Run(fmt.Sprintf("external-%t", external), func(t *testing.T) {
			source, root := t.TempDir(), t.TempDir()
			target := filepath.Join(root, "pages")
			linked := filepath.Join(target, "real")
			if external {
				linked = t.TempDir()
			}
			for _, dir := range []string{filepath.Join(source, "sub"), target, linked} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(source, "sub", "page.txt"), []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(linked, "page.txt")
			if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			linkTarget := "real"
			if external {
				linkTarget = linked
			}
			alias := filepath.Join(target, "sub")
			if err := os.Symlink(linkTarget, alias); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			_, err := SyncSidecarTree(context.Background(), SidecarTreeOptions{SourceDir: source, RootDir: root, TargetDir: "pages"})
			if err == nil {
				t.Error("destination directory symlink accepted")
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != "sentinel" {
				t.Errorf("linked file changed: %q, %v", got, err)
			}
			if _, err := os.Lstat(alias); err != nil {
				t.Errorf("rejected alias pruned: %v", err)
			}
		})
	}
}
