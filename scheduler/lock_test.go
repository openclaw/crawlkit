package scheduler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/internal/filelock"
)

func TestSchedulerLockRepeatedCleanupPreservesOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	releaseA, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	releaseA()
	releaseB, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	releaseA()
	if releaseC, err := acquireLock(path); err == nil {
		releaseC()
		t.Error("repeated old cleanup exposed the current owner")
	}
	releaseB()
	last, err := os.Stat(path)
	if err != nil || !os.SameFile(first, last) {
		t.Fatalf("lock inode removed or replaced: %v", err)
	}
}

func TestSchedulerLockRejectsLegacyMetadata(t *testing.T) {
	for _, data := range []string{"", "pid=", "pid=0\n", fmt.Sprintf("pid=%d\n", os.Getpid()), "unknown-protocol\n"} {
		t.Run(fmt.Sprintf("%q", data), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lock")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if release, err := acquireLock(path); err == nil {
				release()
				t.Fatal("legacy or ambiguous lock accepted without stopped-runner upgrade")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != data {
				t.Fatalf("legacy metadata changed: %q, %v", got, err)
			}
		})
	}
}

func TestSchedulerLockSubprocessLifecycle(t *testing.T) {
	for _, mode := range []string{"release", "exit", "empty", "partial"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lock")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSchedulerLockProcessHelper$", "--", path, mode)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					cancel()
					_ = cmd.Wait()
				}
			}()
			line, err := bufio.NewReader(out).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "locked" {
				t.Fatalf("child acquisition: %q, %v, %s", line, err, stderr.String())
			}
			if release, err := acquireLock(path); err == nil {
				release()
				t.Fatal("second process entered a held lock")
			}
			if _, err := in.Write([]byte("finish\n")); err != nil {
				t.Fatal(err)
			}
			_ = in.Close()
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatalf("child exit: %v: %s", err, stderr.String())
			}
			release, err := acquireLock(path)
			if mode == "empty" || mode == "partial" {
				if err == nil {
					release()
					t.Fatal("ambiguous metadata accepted after holder exit")
				}
				return
			}
			if err != nil {
				t.Fatalf("could not acquire after child %s: %v", mode, err)
			}
			release()
		})
	}
}

func TestSchedulerLockProcessHelper(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 {
		return
	}
	path, mode := os.Args[i+1], os.Args[i+2]
	var release func()
	var err error
	if mode == "empty" || mode == "partial" {
		lock, lockErr := filelock.Acquire(path)
		err = lockErr
		if err == nil {
			release = func() { _ = lock.Close() }
			if mode == "partial" {
				_, err = lock.File.WriteAt([]byte("crawlkit-os-"), 0)
			}
		}
	} else {
		release, err = acquireLock(path)
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if mode == "exit" {
		os.Exit(0)
	}
	release()
}

func TestSchedulerLockRejectsNonregularAndLinkedAliases(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target, lockPath := filepath.Join(dir, "target"), filepath.Join(dir, "lock")
			original := []byte("retained unrelated data\n")
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(lockPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, lockPath); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "hardlink":
				if err := os.Link(target, lockPath); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			}
			if release, err := acquireLock(lockPath); err == nil {
				release()
				t.Fatal("unsafe lock path accepted")
			}
			if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, original) {
				t.Fatalf("alias target changed: %q, %v", data, err)
			}
		})
	}
}
