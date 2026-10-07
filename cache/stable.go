package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultStableAttempts is the attempt count CopyStable uses when
// StableOptions.Attempts is not positive.
const DefaultStableAttempts = 5

// StableOptions tunes CopyStable and CopyStableFiles. The zero value is valid.
type StableOptions struct {
	// Attempts is how many times the copy is tried before the source is
	// reported busy. Nonpositive means DefaultStableAttempts.
	Attempts int
	// Settle is a pause between the last byte copied and the final stat of the
	// source, so a write through a memory mapping that has not yet moved the
	// modification time can show up. Zero skips the pause.
	Settle time.Duration
	// RetryWait is the pause before attempt n+1 after attempt n found the
	// source changing. Zero retries immediately.
	RetryWait time.Duration
	// MaxBytes bounds the total size of the source files. Nonpositive means
	// unbounded.
	MaxBytes int64
	// CheckFreeSpace requires the temp filesystem to have room for the
	// copy plus FreeSpaceHeadroom bytes before each attempt. Where the free
	// space cannot be measured the check is skipped.
	CheckFreeSpace    bool
	FreeSpaceHeadroom int64
	// TempDir is the parent of the private copy directory. Empty means the
	// system temp directory.
	TempDir string
	// AllowMissing skips a source file that does not exist or disappears
	// during an attempt, for stores that delete files while they run. It is
	// never applied to a single-file CopyStable.
	AllowMissing bool
	// Verify checks the finished copies, in source order, after the source
	// proved stable. A failure ends the call without further attempts.
	Verify func(ctx context.Context, copies []string) error

	// afterCopy runs after each attempt's bytes are copied and before the
	// source is stat-ed again. Tests use it to simulate a live writer.
	afterCopy func(attempt int)
	// beforeAttempt runs at the start of each attempt, and afterStat after
	// the preflight stats and before any source is opened.
	beforeAttempt func(attempt int)
	afterStat     func(attempt int)
}

// SourceBusyError reports that the source changed (size, modification time or
// file identity) during every attempt, so no consistent copy was made. Callers
// can retry later.
type SourceBusyError struct {
	Path     string
	Attempts int
	// Reason describes the last change observed: sizes and times only.
	Reason string
}

func (e *SourceBusyError) Error() string {
	return fmt.Sprintf("cache: source changed during all %d copy attempts: %s (%s)", e.Attempts, e.Path, e.Reason)
}

// InsufficientSpaceError reports that the temp filesystem cannot hold a copy.
type InsufficientSpaceError struct {
	Dir   string
	Need  uint64 // bytes required, including headroom
	Avail uint64
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf("cache: need %d bytes free in %s for a stable copy, have %d", e.Need, e.Dir, e.Avail)
}

// CopyStable takes a stable private copy of one live file and returns its
// path. It is CopyStableFiles with a single source.
func CopyStable(ctx context.Context, src string, opts StableOptions) (path string, cleanup func(), err error) {
	opts.AllowMissing = false
	_, paths, cleanup, err := CopyStableFiles(ctx, []string{src}, opts)
	if err != nil {
		return "", cleanup, err
	}
	return paths[0], cleanup, nil
}

// CopyStableFiles copies live files, in the order given, into a new private
// directory (mode 0700, files 0600; a current-user-only ACL on Windows) and returns that directory and the copies
// in source order. Copies keep their source base names, which must be unique.
// The order is the caller's: copy the files a store reads first (data) before
// the files that point at them (manifests).
//
// An attempt is accepted only when every source has the same size,
// modification time and file identity (where the platform exposes one) before
// and after the copy, and the bytes copied equal the size. Otherwise the
// attempt is discarded and retried, up to opts.Attempts, then a
// *SourceBusyError is returned. This gives a coherent copy of a file that a
// writer rewrites in place only if the writer's change is visible to stat; use
// Settle for lazily flushed writes and Verify for provider-specific checks.
//
// cleanup is never nil and is idempotent. Cleanup is also attempted on every
// error, a panic, and when ctx is cancelled, even after success. Close open copy
// handles before final cleanup; if the OS blocks deletion, cleanup can be retried.
func CopyStableFiles(ctx context.Context, srcs []string, opts StableOptions) (dir string, paths []string, cleanup func(), err error) {
	noop := func() {}
	if len(srcs) == 0 {
		return "", nil, noop, errors.New("cache: no source files")
	}
	seen := map[string]bool{}
	for _, s := range srcs {
		b := filepath.Base(s)
		if s == "" || b == "." || b == string(filepath.Separator) || seen[b] {
			return "", nil, noop, fmt.Errorf("cache: source needs a unique file name: %q", s)
		}
		seen[b] = true
	}
	attempts := opts.Attempts
	if attempts <= 0 {
		attempts = DefaultStableAttempts
	}
	tmp, err := makeStableDir(opts.TempDir)
	if err != nil {
		return "", nil, noop, fmt.Errorf("cache: create stable copy dir: %w", err)
	}
	var cleanupMu sync.Mutex
	removed := false
	remove := func() {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		if !removed {
			removed = os.RemoveAll(tmp) == nil
		}
	}
	succeeded := false
	defer func() {
		if !succeeded {
			remove()
		}
	}()

	var last string
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", nil, remove, err
		}
		copies, changed, err := copyStableOnce(ctx, srcs, tmp, attempt, opts)
		if err != nil {
			return "", nil, remove, err
		}
		if changed == "" {
			if opts.Verify != nil {
				if err := opts.Verify(ctx, copies); err != nil {
					return "", nil, remove, err
				}
			}
			if err := ctx.Err(); err != nil {
				return "", nil, remove, err
			}
			succeeded = true
			stop := context.AfterFunc(ctx, remove)
			return tmp, copies, func() { stop(); remove() }, nil
		}
		last = changed
		if attempt < attempts {
			if err := sleepContext(ctx, opts.RetryWait); err != nil {
				return "", nil, remove, err
			}
		}
	}
	return "", nil, remove, &SourceBusyError{Path: srcs[0], Attempts: attempts, Reason: last}
}

type stableSource struct {
	path   string
	before fs.FileInfo
	opened fs.FileInfo
	copy   string
	n      int64
}

// copyStableOnce makes one attempt. A non-empty changed means a source moved
// during the copy and a fresh attempt may succeed; err is a failure no retry
// fixes.
func copyStableOnce(ctx context.Context, srcs []string, dir string, attempt int, opts StableOptions) (copies []string, changed string, err error) {
	if opts.beforeAttempt != nil {
		opts.beforeAttempt(attempt)
	}
	if err := clearDir(dir); err != nil {
		return nil, "", err
	}
	var files []*stableSource
	var total int64
	for _, p := range srcs {
		info, err := statStableSource(p)
		if err != nil {
			if opts.AllowMissing && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, "", fmt.Errorf("stat source: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, "", fmt.Errorf("source is not a regular file: %s", p)
		}
		total += info.Size()
		files = append(files, &stableSource{path: p, before: info})
	}
	if opts.MaxBytes > 0 && total > opts.MaxBytes {
		return nil, "", fmt.Errorf("source files are %d bytes, exceed limit %d", total, opts.MaxBytes)
	}
	if opts.CheckFreeSpace {
		avail, measured, err := freeBytes(dir)
		if err != nil {
			return nil, "", fmt.Errorf("measure free space: %w", err)
		}
		need := uint64(total)
		if opts.FreeSpaceHeadroom > 0 {
			need += uint64(opts.FreeSpaceHeadroom)
		}
		if measured && avail < need {
			return nil, "", &InsufficientSpaceError{Dir: dir, Need: need, Avail: avail}
		}
	}
	if opts.afterStat != nil {
		opts.afterStat(attempt)
	}

	for i := 0; i < len(files); i++ {
		f := files[i]
		missing, reason, err := copyStableFile(ctx, f, dir)
		if err != nil {
			return nil, "", err
		}
		if missing {
			if !opts.AllowMissing {
				// A live writer removed it after the preflight stat: retry.
				return nil, filepath.Base(f.path) + " disappeared between stat and open", nil
			}
			files = append(files[:i], files[i+1:]...)
			i--
			continue
		}
		if reason != "" {
			return nil, reason, nil
		}
	}
	if opts.afterCopy != nil {
		opts.afterCopy(attempt)
	}
	if err := sleepContext(ctx, opts.Settle); err != nil {
		return nil, "", err
	}
	for _, f := range files {
		after, err := statStableSource(f.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if opts.AllowMissing {
					if rerr := os.Remove(f.copy); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
						return nil, "", fmt.Errorf("remove copy of vanished source: %w", rerr)
					}
					continue
				}
				return nil, fmt.Sprintf("%s disappeared during the copy", filepath.Base(f.path)), nil
			}
			return nil, "", fmt.Errorf("stat source: %w", err)
		}
		name := filepath.Base(f.path)
		switch {
		case !os.SameFile(f.before, after):
			return nil, name + " was replaced", nil
		case f.before.Size() != after.Size() || f.opened.Size() != f.before.Size():
			return nil, fmt.Sprintf("%s size %d before, %d at open, %d after", name, f.before.Size(), f.opened.Size(), after.Size()), nil
		case !f.before.ModTime().Equal(after.ModTime()) || !f.opened.ModTime().Equal(f.before.ModTime()):
			return nil, fmt.Sprintf("%s modification time moved %s during the copy", name, after.ModTime().Sub(f.before.ModTime())), nil
		case f.n != f.before.Size():
			return nil, fmt.Sprintf("%s copied %d of %d bytes", name, f.n, f.before.Size()), nil
		}
		copies = append(copies, f.copy)
	}
	return copies, "", nil
}

// copyStableFile copies one source. missing reports a file that vanished
// before it could be opened; reason is a non-empty retryable change.
func copyStableFile(ctx context.Context, f *stableSource, dir string) (missing bool, reason string, err error) {
	in, err := os.Open(f.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, "", nil
		}
		return false, "", fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }()
	opened, err := in.Stat()
	if err != nil {
		return false, "", fmt.Errorf("stat open source: %w", err)
	}
	if !opened.Mode().IsRegular() {
		return false, "", fmt.Errorf("source is not a regular file: %s", f.path)
	}
	if !os.SameFile(opened, f.before) {
		return false, filepath.Base(f.path) + " was swapped between stat and open", nil
	}
	f.opened = opened
	// The size and space checks approved before.Size(). A source that grew
	// since then is unstable: report it before writing anything.
	if opened.Size() != f.before.Size() {
		return false, fmt.Sprintf("%s size %d before, %d at open", filepath.Base(f.path), f.before.Size(), opened.Size()), nil
	}
	f.copy = filepath.Join(dir, filepath.Base(f.path))
	out, err := os.OpenFile(f.copy, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, "", fmt.Errorf("create stable copy: %w", err)
	}
	// Copy at most the approved size plus one byte, so a source that keeps
	// growing cannot outrun the size and space checks; the extra byte shows
	// the growth.
	f.n, err = copyContext(ctx, out, io.LimitReader(in, f.before.Size()+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, "", err
		}
		return false, "", fmt.Errorf("copy source: %w", err)
	}
	if f.n > f.before.Size() {
		return false, fmt.Sprintf("%s grew past %d bytes during the copy", filepath.Base(f.path), f.before.Size()), nil
	}
	return false, "", nil
}

func clearDir(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read stable copy dir: %w", err)
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clear stable copy dir: %w", err)
		}
	}
	return nil
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
