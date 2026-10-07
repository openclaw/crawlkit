//go:build !darwin && !linux && !windows

package cache

// freeBytes reports that free space cannot be measured on this platform.
func freeBytes(string) (avail uint64, measured bool, err error) { return 0, false, nil }
