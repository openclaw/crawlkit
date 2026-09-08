package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAcquireKeepsOnePrivateInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if !first.Created {
		t.Fatal("first lock was not newly created")
	}
	before, err := first.File.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		t.Fatalf("lock permissions = %o", before.Mode().Perm())
	}
	if other, err := Acquire(path); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("second acquisition = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Created {
		t.Fatal("release removed lock file")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := Acquire(path); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("repeated cleanup exposed next holder: %v", err)
	}
	after, err := second.File.Stat()
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode changed: %v", err)
	}
}
