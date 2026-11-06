package sessionowner

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

// DecodeJSON rejects duplicate decoded keys, invalid UTF-8/surrogates and trailing data.
// Limits concern bytes, not character counts. No error contains input data.
func DecodeJSON(b []byte, limit int) (any, error) {
	if limit < 1 || limit > HardFrameLimit || len(b) > limit {
		return nil, fail(PayloadTooLarge, "frame")
	}
	if !utf8.Valid(b) || !validSurrogates(b) {
		return nil, fail(ProtocolMismatch, "utf8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	nodes := 0
	v, err := decodeValue(d, 0, &nodes)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fail(ProtocolMismatch, "trailing")
	}
	return v, nil
}
func decodeValue(d *json.Decoder, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > MaxDepth || *nodes > MaxNodes {
		return nil, fail(PayloadTooLarge, "structure")
	}
	t, err := d.Token()
	if err != nil {
		return nil, fail(ProtocolMismatch, "json")
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, fail(ProtocolMismatch, "key")
				}
				s, ok := k.(string)
				if !ok {
					return nil, fail(ProtocolMismatch, "key")
				}
				if _, exists := m[s]; exists {
					return nil, fail(ProtocolMismatch, "duplicate_key")
				}
				v, err := decodeValue(d, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				m[s] = v
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, fail(ProtocolMismatch, "object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := decodeValue(d, depth+1, nodes)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, fail(ProtocolMismatch, "array")
			}
			return a, nil
		default:
			return nil, fail(ProtocolMismatch, "delimiter")
		}
	}
	if n, ok := t.(json.Number); ok {
		v, e := strconv.ParseFloat(string(n), 64)
		if e != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, fail(ProtocolMismatch, "number")
		}
		// Larger integers require a registry-qualified string/binary representation.
		if math.Trunc(v) == v && math.Abs(v) > 9007199254740991 {
			return nil, fail(ProtocolMismatch, "lossless_number")
		}
		return json.Number(strconv.FormatFloat(v, 'g', -1, 64)), nil
	}
	return t, nil
}
func validSurrogates(b []byte) bool {
	in := false
	for i := 0; i < len(b); i++ {
		if !in {
			if b[i] == '"' {
				in = true
			}
			continue
		}
		if b[i] == '"' {
			in = false
			continue
		}
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		x, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if x >= 0xdc00 && x <= 0xdfff {
			return false
		}
		if x >= 0xd800 && x <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			y, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || y < 0xdc00 || y > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return !in
}

func ReadFrame(r io.Reader, capBytes int) ([]byte, error) {
	if capBytes < 1 || capBytes > HardFrameLimit {
		return nil, fail(ProtocolMismatch, "cap")
	}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || uint64(n) > uint64(capBytes) {
		return nil, fail(PayloadTooLarge, "frame")
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}
func WriteFrame(w io.Writer, b []byte, capBytes int) error {
	if len(b) == 0 || len(b) > capBytes || capBytes > HardFrameLimit {
		return fail(PayloadTooLarge, "frame")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if err := writeAll(w, h[:]); err != nil {
		return err
	}
	return writeAll(w, b)
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// CanonicalJSON is used only on already validated, secret-private owner data.
func CanonicalJSON(v any) ([]byte, error)    { return json.Marshal(v) }
func object(v any) (map[string]any, bool)    { m, ok := v.(map[string]any); return m, ok }
func text(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func num(v any) float64 {
	n, ok := v.(json.Number)
	if !ok {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(string(n), 64)
	return f
}
