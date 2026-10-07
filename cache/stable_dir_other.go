//go:build !windows

package cache

import "os"

func makeStableDir(parent string) (string, error) {
	return os.MkdirTemp(parent, "crawlkit-stable-")
}
