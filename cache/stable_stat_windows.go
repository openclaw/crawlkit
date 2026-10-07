package cache

import "os"

func statStableSource(path string) (os.FileInfo, error) {
	// Path-based Stat loads identity lazily in SameFile; capture it now so a
	// later replacement cannot become the identity of the preflight result.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Stat()
}
