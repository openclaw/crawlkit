// Package filelock holds a nonblocking OS lock on a persistent regular file.
// All users must keep the pathname and inode in place, including on release.
package filelock

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var ErrLocked = errors.New("already locked")

type Lock struct {
	File    *os.File
	Created bool
	once    sync.Once
	err     error
}

func Acquire(path string) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("lock path is not a regular file: %s", path)
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Lock, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	entry, err := os.Lstat(path)
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || !entry.Mode().IsRegular() || !os.SameFile(info, entry) {
		return fail(fmt.Errorf("lock path changed or is not a regular file: %s", path))
	}
	if err := lockFile(file); err != nil {
		return fail(err)
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(err)
	}
	return &Lock{File: file, Created: created}, nil
}

// Close releases the kernel lock by closing its owning handle. It is idempotent
// and never unlinks the file, so old cleanup cannot expose a later owner.
func (lock *Lock) Close() error {
	lock.once.Do(func() { lock.err = lock.File.Close() })
	return lock.err
}
