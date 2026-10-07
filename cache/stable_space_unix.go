//go:build darwin || linux

package cache

import "golang.org/x/sys/unix"

// freeBytes returns the bytes available to an unprivileged writer in dir.
func freeBytes(dir string) (avail uint64, measured bool, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true, nil //nolint:gosec // block counts are non-negative
}
