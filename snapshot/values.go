package snapshot

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxExactJSONInteger = 1<<53 - 1

// Decode with exact tokens first, then restore the ordinary legacy callback
// types. Only integers that float64 cannot safely carry become native int64.
func decodeSnapshotRow(data []byte, legacyWriter bool) (map[string]any, error) {
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON row")
	}
	if err := validateJSONText(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var row map[string]any
	if err := decoder.Decode(&row); err != nil {
		return nil, err
	}
	_, err := normalizeJSONValues(row, legacyWriter)
	return row, err
}

func normalizeJSONValues(value any, legacyWriter bool) (any, error) {
	switch v := value.(type) {
	case json.Number:
		return snapshotNumber(v.String(), legacyWriter)
	case map[string]any:
		for key, item := range v {
			normalized, err := normalizeJSONValues(item, legacyWriter)
			if err != nil {
				return nil, err
			}
			v[key] = normalized
		}
	case []any:
		for i, item := range v {
			normalized, err := normalizeJSONValues(item, legacyWriter)
			if err != nil {
				return nil, err
			}
			v[i] = normalized
		}
	}
	return value, nil
}

func snapshotNumber(token string, legacyWriter bool) (any, error) {
	// Bound exact decimal work even for adversarial, otherwise valid JSON.
	if len(token) > 4096 {
		return nil, errors.New("JSON number exceeds supported precision")
	}
	f, err := strconv.ParseFloat(token, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, errors.New("JSON number is outside float64 range")
	}
	if f == 0 {
		mantissa, _, _ := strings.Cut(strings.ToLower(token), "e")
		if strings.ContainsAny(mantissa, "123456789") {
			return nil, errors.New("JSON number underflows float64")
		}
		return f, nil
	}
	exact, ok := new(big.Rat).SetString(token)
	if !ok {
		return nil, errors.New("unsupported JSON number")
	}
	if exact.IsInt() {
		if !exact.Num().IsInt64() {
			return nil, errors.New("JSON integer is outside int64 range")
		}
		n := exact.Num().Int64()
		if n < -maxExactJSONInteger || n > maxExactJSONInteger {
			if legacyWriter {
				return nil, errors.New("v1 snapshot cannot safely export integers outside +/- (2^53-1); filter or explicitly encode the value as text")
			}
			return n, nil
		}
		return f, nil
	}
	if exact.Abs(exact).Cmp(new(big.Rat).SetInt64(maxExactJSONInteger)) > 0 {
		return nil, errors.New("JSON fractional number exceeds safe float64 integer precision")
	}
	return f, nil
}

// encoding/json replaces invalid UTF-8 and unpaired UTF-16 escapes. A snapshot
// must refuse them instead of committing replacement characters to the archive.
func validateJSONText(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("snapshot text is not valid UTF-8")
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
			return errors.New("snapshot text contains an unpaired UTF-16 surrogate")
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return errors.New("snapshot text contains an unpaired UTF-16 surrogate")
		}
		low, _ := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if low < 0xdc00 || low > 0xdfff {
			return errors.New("snapshot text contains an unpaired UTF-16 surrogate")
		}
		i += 6
	}
	return nil
}

func encodeSnapshotRow(row map[string]any, blobs map[string]string) ([]byte, error) {
	for col, original := range blobs {
		if text, ok := row[col].(string); ok && text == original {
			return nil, fmt.Errorf("v1 snapshot cannot safely export BLOB column %s; filter or explicitly replace its representation", col)
		}
	}
	data, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	// Marshal detects cycles/unsupported values first. Inspect original strings
	// before accepting its output, since the encoder silently repairs UTF-8.
	if err := validateExportValue(reflect.ValueOf(row), 0); err != nil {
		return nil, err
	}
	if _, err := decodeSnapshotRow(data, true); err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func validateExportValue(value reflect.Value, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > 1000 {
		return errors.New("snapshot value exceeds supported nesting")
	}
	if value.CanInterface() {
		// Explicit caller encoders own their transformation; their JSON output
		// still passes the same numeric and text validation above.
		if _, ok := value.Interface().(json.Marshaler); ok {
			return nil
		}
		if _, ok := value.Interface().(encoding.TextMarshaler); ok {
			return nil
		}
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return validateExportValue(value.Elem(), depth+1)
		}
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("snapshot text is not valid UTF-8")
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return errors.New("v1 snapshot cannot safely export BLOB values")
		}
		for i := 0; i < value.Len(); i++ {
			if err := validateExportValue(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := validateExportValue(iter.Key(), depth+1); err != nil {
				return err
			}
			if err := validateExportValue(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			if err := validateExportValue(value.Field(i), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
