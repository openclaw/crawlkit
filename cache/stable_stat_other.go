//go:build !windows

package cache

import "os"

func statStableSource(path string) (os.FileInfo, error) { return os.Stat(path) }
