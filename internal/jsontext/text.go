package jsontext

import (
	"errors"
	"strconv"
	"unicode/utf8"
)

// Validate rejects text that encoding/json would silently replace. The caller
// must establish valid JSON syntax before calling; this function checks text only.
func Validate(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("text is not valid UTF-8")
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		code, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("text contains an unpaired UTF-16 surrogate")
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return errors.New("text contains an unpaired UTF-16 surrogate")
		}
		low, _ := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if low < 0xdc00 || low > 0xdfff {
			return errors.New("text contains an unpaired UTF-16 surrogate")
		}
		i += 6
	}
	return nil
}
