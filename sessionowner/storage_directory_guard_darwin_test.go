//go:build darwin

package sessionowner

import (
	"os"
	"path/filepath"
	"testing"
)

func c5EmptyAuthorityRoot(t *testing.T, path string) {
	t.Helper()
	entries, e := os.ReadDir(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 0 {
		t.Fatal("backend constructor created authority files")
	}
}
func TestC5D1OwnedRootMode(t *testing.T) {
	// Only this freshly allocated owned directory changes mode. Existing TMPDIR
	// and every ancestor remain untouched, using the current owner test route.
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := os.Chmod(root, 0700); e != nil {
			t.Error(e)
		}
	})
	good, e := NewFileBackend(FileOptions{Directory: root, BindingID: "fixture-binding"})
	if e != nil || good == nil {
		t.Fatal("approved owned-temp positive control failed", e)
	}
	c5EmptyAuthorityRoot(t, root)
	if e = os.Chmod(root, 0755); e != nil {
		t.Fatal(e)
	}
	bad, e := NewFileBackend(FileOptions{Directory: root, BindingID: "fixture-binding"})
	typed, ok := e.(*Error)
	if bad != nil || !ok || typed.Code != UnsafeStorage || typed.Detail != "directory_mode" {
		t.Fatal("wrong root mode did not reach directory_mode", e)
	}
	c5EmptyAuthorityRoot(t, root)
	if e = os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = NewFileBackend(FileOptions{Directory: root, BindingID: "fixture-binding"}); e != nil {
		t.Fatal("restored owned root refused", e)
	}
	c5EmptyAuthorityRoot(t, root)
	t.Log("C5_CONTROL D1 positive0700,wrongroot0755=>UnsafeStorage/directory_mode,restored0700;authorityfiles=0;onlynewownedrootchmod")
}
func TestC5D2OwnedParentMode(t *testing.T) {
	parent := t.TempDir()
	if e := os.Chmod(parent, 0700); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(parent, "authority")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := os.Chmod(parent, 0700); e != nil {
			t.Error(e)
		}
	})
	good, e := NewFileBackend(FileOptions{Directory: root, BindingID: "fixture-binding"})
	if e != nil || good == nil {
		t.Fatal("approved owned-parent positive control failed", e)
	}
	c5EmptyAuthorityRoot(t, root)
	if e = os.Chmod(parent, 0770); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(root)
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatal("fixture changed root instead of newparent", e)
	}
	bad, e := NewFileBackend(FileOptions{Directory: root, BindingID: "fixture-binding"})
	typed, ok := e.(*Error)
	if bad != nil || !ok || typed.Code != UnsafeStorage || typed.Detail != "ancestor_owner" {
		t.Fatal("owned writable parent did not reach ancestor_owner", e)
	}
	c5EmptyAuthorityRoot(t, root)
	entries, e := os.ReadDir(parent)
	if e != nil || len(entries) != 1 || entries[0].Name() != "authority" {
		t.Fatal("parent effects outside owned root", e)
	}
	if e = os.Chmod(parent, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = NewFileBackend(FileOptions{Directory: root, BindingID: "fixture-binding"}); e != nil {
		t.Fatal("restored owned parent refused", e)
	}
	c5EmptyAuthorityRoot(t, root)
	t.Log("C5_CONTROL D2 parent0700/root0700 positive,parent0770=>UnsafeStorage/ancestor_owner,restoredparent0700;authorityfiles=0;noexistingancestorchmod")
}
