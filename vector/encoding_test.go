package vector

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func TestFloat32EncodingMatchesBinary(t *testing.T) {
	bits := []uint32{0, 1 << 31, 1, 0x80000001, 0x007fffff, 0x00800000, 0x3f800000, 0xbf800000, 0x7f7fffff, 0xff7fffff, 0x7f800000, 0xff800000, 0x7fc00000, 0x7f800001, 0xffc12345}
	rng := rand.New(rand.NewPCG(3, 4))
	for range 4096 {
		bits = append(bits, rng.Uint32())
	}
	values := make([]float32, len(bits))
	for i, value := range bits {
		values[i] = math.Float32frombits(value)
	}
	for _, input := range [][]float32{nil, {}, values} {
		old := bytes.NewBuffer(make([]byte, 0, len(input)*4))
		for _, value := range input {
			if err := binary.Write(old, binary.LittleEndian, value); err != nil {
				t.Fatal(err)
			}
		}
		blob, err := EncodeFloat32(input)
		if err != nil {
			t.Fatal(err)
		}
		if blob == nil || !bytes.Equal(blob, old.Bytes()) {
			t.Fatal("encoding differs")
		}
		decoded, err := DecodeFloat32(blob)
		if err != nil {
			t.Fatal(err)
		}
		if decoded == nil || len(decoded) != len(input) {
			t.Fatal("decoded length/nil differs")
		}
		reader := bytes.NewReader(blob)
		for i, value := range decoded {
			var oldValue float32
			if err := binary.Read(reader, binary.LittleEndian, &oldValue); err != nil {
				t.Fatal(err)
			}
			if math.Float32bits(value) != math.Float32bits(oldValue) || math.Float32bits(value) != math.Float32bits(input[i]) {
				t.Fatalf("bits differ at %d", i)
			}
		}
	}
	for length := 1; length < 20; length++ {
		if length%4 == 0 {
			continue
		}
		values, err := DecodeFloat32(make([]byte, length))
		want := fmt.Sprintf("float32 vector blob length %d is not a multiple of 4", length)
		if values != nil || err == nil || err.Error() != want {
			t.Fatalf("length %d: values=%v err=%v", length, values, err)
		}
	}
}
