//go:build windows

package cache

import "golang.org/x/sys/windows"

// freeBytes returns the bytes available to the calling user in dir.
func freeBytes(dir string) (avail uint64, measured bool, err error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false, err
	}
	return free, true, nil
}
