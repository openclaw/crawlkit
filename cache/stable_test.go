package cache

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCopyStableQuietSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "hello")
	verified := 0
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: t.TempDir(),
		Verify: func(_ context.Context, copies []string) error {
			verified += len(copies)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "hello" {
		t.Fatalf("copy = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %v", st.Mode().Perm())
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %v", st.Mode().Perm())
		}
	}
	if verified != 1 {
		t.Fatalf("verify calls = %d", verified)
	}
	cleanup()
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("copy dir survived cleanup: %v", err)
	}
}

func TestCopyStableRetriesWhenWriterChangesSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v1")
	parent := t.TempDir()
	attempts := 0
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: parent,
		afterCopy: func(attempt int) {
			attempts = attempt
			if attempt < 3 {
				writeFile(t, src, strings.Repeat("x", attempt+5)) // writer grows the file
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got, _ := os.ReadFile(path); string(got) != strings.Repeat("x", 7) {
		t.Fatalf("copy = %q", got)
	}
}

func TestCopyStableDetectsModTimeOnlyChange(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "same")
	n := 0
	_, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: t.TempDir(),
		afterCopy: func(attempt int) {
			n = attempt
			if attempt == 1 {
				_ = os.Chtimes(src, time.Now(), time.Now().Add(time.Hour))
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

func TestCopyStableRetriesOnReplacementWithSameSizeAndModTime(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "old")
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(src), "replacement")
	writeFile(t, replacement, "new")
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: t.TempDir(),
		afterStat: func(attempt int) {
			attempts = attempt
			if attempt == 1 {
				if err := os.Rename(replacement, src); err != nil {
					t.Fatal(err)
				}
			}
		},
	})
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want a retry after source replacement", attempts)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("copy = %q, err = %v", b, err)
	}
}

func TestCopyStableSourceBusyAfterAttempts(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	parent := t.TempDir()
	calls := 0
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		Attempts: 3,
		TempDir:  parent,
		afterCopy: func(attempt int) {
			calls++
			writeFile(t, src, strings.Repeat("x", attempt+1))
		},
	})
	var busy *SourceBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want SourceBusyError", err)
	}
	if busy.Attempts != 3 || calls != 3 || busy.Path != src || busy.Reason == "" {
		t.Fatalf("busy = %+v calls=%d", busy, calls)
	}
	if path != "" || cleanup == nil {
		t.Fatalf("path=%q cleanup nil=%v", path, cleanup == nil)
	}
	cleanup()
	assertEmptyDir(t, parent)
}

func TestCopyStableDefaultsToFiveAttempts(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	calls := 0
	_, _, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: t.TempDir(),
		afterCopy: func(attempt int) {
			calls++
			writeFile(t, src, strings.Repeat("x", attempt+1))
		},
	})
	var busy *SourceBusyError
	if !errors.As(err, &busy) || calls != DefaultStableAttempts || busy.Attempts != DefaultStableAttempts {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestCopyStableCleansUpOnContextCancel(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := CopyStable(ctx, src, StableOptions{
		TempDir:   parent,
		Settle:    time.Minute,
		afterCopy: func(int) { cancel() },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyDir(t, parent)
}

func TestCopyStableCancelAfterSuccessRemovesCopy(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	ctx, cancel := context.WithCancel(context.Background())
	path, cleanup, err := CopyStable(ctx, src, StableOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("copy survived context cancel")
}

func TestCopyStableCleanupRetriesAfterRemoveFailure(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "private copy")
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dir := filepath.Dir(path)
	var release func()
	if runtime.GOOS == "windows" {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		release = func() { _ = f.Close() }
	} else {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		release = func() { _ = os.Chmod(dir, 0o700) }
	}
	defer release()
	cleanup()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("copy should remain while deletion is blocked: %v", err)
	}
	release()
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("copy dir survived a cleanup retry: %v", err)
	}
}

func TestCopyStableVerifyFailureCleansUp(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	parent := t.TempDir()
	want := errors.New("bad copy")
	calls := 0
	_, _, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: parent,
		Verify:  func(context.Context, []string) error { calls++; return want },
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	assertEmptyDir(t, parent)
}

func TestCopyStableBounds(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "12345")
	parent := t.TempDir()
	if _, _, err := CopyStable(context.Background(), src, StableOptions{TempDir: parent, MaxBytes: 4}); err == nil {
		t.Fatal("expected size limit error")
	}
	assertEmptyDir(t, parent)
	if _, c, err := CopyStable(context.Background(), src, StableOptions{TempDir: parent, MaxBytes: 5}); err != nil {
		t.Fatal(err)
	} else {
		c()
	}
	if _, _, err := CopyStable(context.Background(), filepath.Join(parent, "absent"), StableOptions{TempDir: parent}); err == nil {
		t.Fatal("expected missing source error")
	}
	assertEmptyDir(t, parent)
}

func TestCopyStableFreeSpaceCheck(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("free space is not measured on this platform")
	}
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	parent := t.TempDir()
	_, _, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: parent, CheckFreeSpace: true, FreeSpaceHeadroom: math.MaxInt64,
	})
	var space *InsufficientSpaceError
	if !errors.As(err, &space) {
		t.Fatalf("err = %v", err)
	}
	assertEmptyDir(t, parent)
	_, c, err := CopyStable(context.Background(), src, StableOptions{TempDir: parent, CheckFreeSpace: true})
	if err != nil {
		t.Fatal(err)
	}
	c()
}

func TestCopyStableFilesKeepsCallerOrderAndAllowsMissing(t *testing.T) {
	srcDir := t.TempDir()
	order := []string{"000001.ldb", "000002.log", "MANIFEST-000003", "CURRENT"}
	var srcs []string
	for _, n := range order {
		writeFile(t, filepath.Join(srcDir, n), "data-"+n)
		srcs = append(srcs, filepath.Join(srcDir, n))
	}
	gone := filepath.Join(srcDir, "000009.ldb")
	srcs = append([]string{gone}, srcs...)
	dir, copies, cleanup, err := CopyStableFiles(context.Background(), srcs, StableOptions{TempDir: t.TempDir(), AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(copies) != len(order) {
		t.Fatalf("copies = %v", copies)
	}
	for i, n := range order {
		if copies[i] != filepath.Join(dir, n) {
			t.Fatalf("copies[%d] = %s", i, copies[i])
		}
		if b, _ := os.ReadFile(copies[i]); string(b) != "data-"+n {
			t.Fatalf("copy %s = %q", n, b)
		}
	}
	if _, _, c, err := CopyStableFiles(context.Background(), srcs, StableOptions{TempDir: t.TempDir()}); err == nil {
		t.Fatal("missing file must fail without AllowMissing")
	} else {
		c()
	}
}

func TestCopyStableFilesRetriesOnAnyFileChanging(t *testing.T) {
	srcDir := t.TempDir()
	a, b := filepath.Join(srcDir, "a"), filepath.Join(srcDir, "CURRENT")
	writeFile(t, a, "a")
	writeFile(t, b, "b")
	n := 0
	_, copies, cleanup, err := CopyStableFiles(context.Background(), []string{a, b}, StableOptions{
		TempDir: t.TempDir(),
		afterCopy: func(attempt int) {
			n = attempt
			if attempt == 1 {
				writeFile(t, b, "bbbb")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got, _ := os.ReadFile(copies[1]); n != 2 || string(got) != "bbbb" {
		t.Fatalf("attempts=%d CURRENT=%q", n, got)
	}
}

func TestCopyStableFilesRejectsBadInput(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "x"), "x")
	if _, _, _, err := CopyStableFiles(context.Background(), nil, StableOptions{}); err == nil {
		t.Fatal("empty sources must fail")
	}
	other := t.TempDir()
	writeFile(t, filepath.Join(other, "x"), "y")
	if _, _, _, err := CopyStableFiles(context.Background(), []string{filepath.Join(d, "x"), filepath.Join(other, "x")}, StableOptions{}); err == nil {
		t.Fatal("duplicate base names must fail")
	}
	if _, _, err := CopyStable(context.Background(), d, StableOptions{TempDir: t.TempDir()}); err == nil {
		t.Fatal("directory source must fail")
	}
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("%s not empty: %v", dir, ents)
	}
}

func TestCopyStableFileDoesNotWritePastApprovedSize(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "approved")
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	// The source grows on the same inode between the preflight stat and open.
	f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("g", 4096)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	dir := t.TempDir()
	_, reason, err := copyStableFile(context.Background(), &stableSource{path: src, before: before}, dir)
	if err != nil || reason == "" {
		t.Fatalf("reason=%q err=%v, want a retryable change", reason, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "store.db")); err == nil && info.Size() > before.Size() {
		t.Fatalf("wrote %d bytes, approved %d", info.Size(), before.Size())
	}
}

func TestCopyStableVerifyCancelReturnsContextError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _, err := CopyStable(ctx, src, StableOptions{
		TempDir: parent,
		Verify:  func(context.Context, []string) error { cancel(); return nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertEmptyDir(t, parent)
}

func TestCopyStableFilesAllowMissingSourceVanishesBeforeFinalCheck(t *testing.T) {
	srcDir := t.TempDir()
	keep := filepath.Join(srcDir, "keep.ldb")
	vanish := filepath.Join(srcDir, "vanish.ldb")
	writeFile(t, keep, "keep")
	writeFile(t, vanish, "vanish")
	dir, copies, cleanup, err := CopyStableFiles(context.Background(), []string{vanish, keep}, StableOptions{
		TempDir:      t.TempDir(),
		Attempts:     1,
		AllowMissing: true,
		afterCopy:    func(int) { _ = os.Remove(vanish) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(copies) != 1 || copies[0] != filepath.Join(dir, "keep.ldb") {
		t.Fatalf("copies = %v", copies)
	}
	if _, err := os.Stat(filepath.Join(dir, "vanish.ldb")); !os.IsNotExist(err) {
		t.Fatalf("copy of vanished source remains: %v", err)
	}
}

func TestCopyStableFilesVanishedSourceWithoutAllowMissingStillRetries(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	_, _, _, err := CopyStableFiles(context.Background(), []string{src}, StableOptions{
		TempDir:   t.TempDir(),
		Attempts:  1,
		afterCopy: func(int) { _ = os.Remove(src) },
	})
	var busy *SourceBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want SourceBusyError", err)
	}
}

func TestCopyStableRetriesWhenSourceVanishesBeforeOpen(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v1")
	path, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: t.TempDir(),
		afterStat: func(attempt int) {
			if attempt == 1 {
				_ = os.Remove(src)
			}
		},
		beforeAttempt: func(attempt int) {
			if attempt == 2 {
				writeFile(t, src, "v2")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got, _ := os.ReadFile(path); string(got) != "v2" {
		t.Fatalf("copy = %q", got)
	}
}

func TestCopyStableSourceBusyWhenSourceKeepsVanishingBeforeOpen(t *testing.T) {
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "v")
	parent := t.TempDir()
	_, _, err := CopyStable(context.Background(), src, StableOptions{
		TempDir:       parent,
		Attempts:      3,
		afterStat:     func(int) { _ = os.Remove(src) },
		beforeAttempt: func(int) { writeFile(t, src, "v") },
	})
	var busy *SourceBusyError
	if !errors.As(err, &busy) || busy.Attempts != 3 {
		t.Fatalf("err = %v, want SourceBusyError after 3 attempts", err)
	}
	assertEmptyDir(t, parent)
}

func TestCopyStableFilesAllowMissingSkipsSourceVanishingBeforeOpen(t *testing.T) {
	srcDir := t.TempDir()
	keep, gone := filepath.Join(srcDir, "keep"), filepath.Join(srcDir, "gone")
	writeFile(t, keep, "k")
	writeFile(t, gone, "g")
	_, copies, cleanup, err := CopyStableFiles(context.Background(), []string{gone, keep}, StableOptions{
		TempDir: t.TempDir(), Attempts: 1, AllowMissing: true,
		afterStat: func(int) { _ = os.Remove(gone) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(copies) != 1 || filepath.Base(copies[0]) != "keep" {
		t.Fatalf("copies = %v", copies)
	}
}
func TestCopyStableErrorCleanupRetriesAfterRemoveFailure(t *testing.T) {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	src := filepath.Join(t.TempDir(), "store.db")
	writeFile(t, src, "private copy")
	parent := t.TempDir()
	want := errors.New("verification failed")
	var release func()
	_, cleanup, err := CopyStable(context.Background(), src, StableOptions{
		TempDir: parent,
		Verify: func(_ context.Context, copies []string) error {
			if runtime.GOOS == "windows" {
				f, err := os.Open(copies[0])
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = f.Close() }
			} else {
				dir := filepath.Dir(copies[0])
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				release = func() { _ = os.Chmod(dir, 0o700) }
			}
			return want
		},
	})
	if release != nil {
		release()
	}
	cleanup()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want verification failure", err)
	}
	assertEmptyDir(t, parent)
}
