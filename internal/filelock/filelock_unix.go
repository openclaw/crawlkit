//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package filelock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) error {
	var info unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &info); err != nil {
		return err
	}
	if info.Nlink != 1 {
		return fmt.Errorf("lock file must not have hardlink aliases")
	}
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLocked
	}
	return err
}
