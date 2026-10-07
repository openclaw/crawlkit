package cache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCopyStableWindowsPrivateACL(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "private copy")
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Dir(path), path} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		if p == filepath.Dir(path) {
			control, _, err := sd.Control()
			if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
				t.Fatalf("copy directory inherits parent access: control=%v err=%v", control, err)
			}
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil || acl.AceCount != 1 {
			t.Fatalf("copy ACL must grant access only to the current user: %v", err)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, 0, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
			t.Fatal("copy ACL grants access to a different principal")
		}
	}
}

func TestCopyStableWindowsLongTempDir(t *testing.T) {
	parent := filepath.Join(t.TempDir(), strings.Repeat("a", 100), strings.Repeat("b", 100), strings.Repeat("c", 100))
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "long-path copy")
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{TempDir: parent})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if b, err := os.ReadFile(path); err != nil || string(b) != "long-path copy" {
		t.Fatalf("copy = %q, err = %v", b, err)
	}
}
