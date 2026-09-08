//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package filelock

import (
	"fmt"
	"os"
	"runtime"
)

func lockFile(*os.File) error {
	return fmt.Errorf("OS file locking is unsupported on %s", runtime.GOOS)
}
