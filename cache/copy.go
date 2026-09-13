package cache

import (
	"errors"
	"io"
)

func copyLimited(dst io.Writer, src io.Reader, limit int64) (copied int64, exceeded bool, err error) {
	if limit <= 0 {
		copied, err = io.Copy(dst, src)
		return copied, false, err
	}
	copied, err = io.Copy(dst, io.LimitReader(src, limit))
	if err != nil || copied < limit {
		return copied, false, err
	}
	// Probe separately so a MaxInt64 limit never overflows.
	extra, err := io.CopyN(io.Discard, src, 1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return copied, extra != 0, err
}
