package scheduler

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestReadHistoryTail(t *testing.T) {
	for _, size := range []int{0, 1, 4095, 4096, 4097, 8192, 12289, 1 << 20} {
		for _, prefix := range []string{"", "first\n", "first\nsecond\n"} {
			t.Run(fmt.Sprintf("size=%d/prefix=%d", size, len(prefix)), func(t *testing.T) {
				tail := bytes.Repeat([]byte("x"), size)
				data := append([]byte(prefix), tail...)
				start, got, err := readHistoryTail(bytes.NewReader(data), int64(len(data)))
				if err != nil || start != int64(len(prefix)) || !bytes.Equal(got, tail) {
					t.Fatalf("tail: start=%d length=%d err=%v; want start=%d length=%d", start, len(got), err, len(prefix), size)
				}
			})
		}
	}
}

func TestReadHistoryTailReadError(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 8193)
	reader := &failingTailReader{Reader: bytes.NewReader(data)}
	_, _, err := readHistoryTail(reader, int64(len(data)))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v, want unexpected EOF", err)
	}
}

type failingTailReader struct {
	*bytes.Reader
	reads int
}

func (r *failingTailReader) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	if r.reads == 2 {
		return 0, io.ErrUnexpectedEOF
	}
	return r.Reader.ReadAt(p, off)
}

func BenchmarkReadHistoryTail(b *testing.B) {
	for _, size := range []int{256, 4096, 64 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			data := append([]byte("previous\n"), bytes.Repeat([]byte("x"), size)...)
			reader := bytes.NewReader(data)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for b.Loop() {
				if _, _, err := readHistoryTail(reader, int64(len(data))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
