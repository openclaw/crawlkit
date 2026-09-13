package remote

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

type sqliteBundleJSONVisit struct {
	kind reflect.Kind
	typ  reflect.Type
	ptr  uintptr
}

type sqliteBundleJSONSizeCounter struct {
	size  int64
	limit int64
	seen  map[sqliteBundleJSONVisit]struct{}
}

func preflightSQLiteBundleManifestEncoding(
	manifest SQLiteBundleManifest,
	limit int64,
) (int64, error) {
	counter := sqliteBundleJSONSizeCounter{
		limit: limit,
		seen:  make(map[sqliteBundleJSONVisit]struct{}),
	}
	if err := counter.addValue(reflect.ValueOf(manifest), 0); err != nil {
		if errors.Is(err, errManifestSizeLimit) {
			return 0, fmt.Errorf("sqlite bundle manifest must not exceed %d bytes", limit)
		}
		return 0, fmt.Errorf("encode sqlite bundle manifest: %w", err)
	}
	return counter.size, nil
}

func (c *sqliteBundleJSONSizeCounter) add(size int64) error {
	if size < 0 || size > c.limit-c.size {
		return errManifestSizeLimit
	}
	c.size += size
	return nil
}

func (c *sqliteBundleJSONSizeCounter) addIndent(depth int) error {
	return c.add(int64(depth * 2))
}

func (c *sqliteBundleJSONSizeCounter) addString(value string) error {
	if err := c.add(2); err != nil {
		return err
	}
	for len(value) > 0 {
		char, width := utf8.DecodeRuneInString(value)
		value = value[width:]
		size := int64(width)
		switch {
		case char == utf8.RuneError && width == 1:
			size = 6
		case char == '\\' || char == '"':
			size = 2
		case char == '\b' || char == '\f' || char == '\n' || char == '\r' || char == '\t':
			size = 2
		case char < 0x20 || char == '\u2028' || char == '\u2029':
			size = 6
		}
		if err := c.add(size); err != nil {
			return err
		}
	}
	return nil
}

func (c *sqliteBundleJSONSizeCounter) addValue(value reflect.Value, depth int) error {
	if depth > maxSQLiteBundleManifestJSONDepth {
		return &json.UnsupportedValueError{
			Value: value,
			Str:   "exceeded maximum sqlite bundle manifest nesting depth",
		}
	}
	if !value.IsValid() {
		return c.add(4)
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return c.add(4)
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return c.add(4)
	}
	if value.Type() == jsonNumberType {
		number := value.String()
		if !validJSONNumber(number) {
			return fmt.Errorf("json: invalid number literal %q", number)
		}
		return c.add(int64(len(number)))
	}
	if value.Type().Implements(jsonMarshalerType) ||
		value.Type().Implements(textMarshalerType) ||
		(value.Kind() != reflect.Pointer &&
			(reflect.PointerTo(value.Type()).Implements(jsonMarshalerType) ||
				reflect.PointerTo(value.Type()).Implements(textMarshalerType))) {
		return &json.UnsupportedTypeError{Type: value.Type()}
	}

	switch value.Kind() {
	case reflect.Pointer:
		if err := c.enter(value); err != nil {
			return err
		}
		defer c.leave(value)
		return c.addValue(value.Elem(), depth)
	case reflect.Bool:
		if value.Bool() {
			return c.add(4)
		}
		return c.add(5)
	case reflect.String:
		return c.addString(value.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return c.add(int64(len(strconv.FormatInt(value.Int(), 10))))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return c.add(int64(len(strconv.FormatUint(value.Uint(), 10))))
	case reflect.Float32, reflect.Float64:
		number, err := sqliteBundleJSONFloat(value.Float(), value.Type().Bits())
		if err != nil {
			return err
		}
		return c.add(int64(len(number)))
	case reflect.Slice:
		if value.IsNil() {
			return c.add(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			length := value.Len()
			if int64(length) > c.limit-c.size {
				return errManifestSizeLimit
			}
			return c.add(int64(base64.StdEncoding.EncodedLen(length) + 2))
		}
		if err := c.enter(value); err != nil {
			return err
		}
		defer c.leave(value)
		return c.addArray(value, depth)
	case reflect.Array:
		return c.addArray(value, depth)
	case reflect.Map:
		if value.IsNil() {
			return c.add(4)
		}
		if value.Type().Key().Kind() != reflect.String ||
			value.Type().Key().Implements(textMarshalerType) {
			return &json.UnsupportedTypeError{Type: value.Type()}
		}
		if err := c.enter(value); err != nil {
			return err
		}
		defer c.leave(value)
		return c.addMap(value, depth)
	case reflect.Struct:
		return c.addStruct(value, depth)
	default:
		return &json.UnsupportedTypeError{Type: value.Type()}
	}
}

func (c *sqliteBundleJSONSizeCounter) addArray(value reflect.Value, depth int) error {
	if value.Len() == 0 {
		return c.add(2)
	}
	if err := c.add(1); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if index == 0 {
			if err := c.add(1); err != nil {
				return err
			}
		} else if err := c.add(2); err != nil {
			return err
		}
		if err := c.addIndent(depth + 1); err != nil {
			return err
		}
		if err := c.addValue(value.Index(index), depth+1); err != nil {
			return err
		}
	}
	if err := c.add(1); err != nil {
		return err
	}
	if err := c.addIndent(depth); err != nil {
		return err
	}
	return c.add(1)
}

func (c *sqliteBundleJSONSizeCounter) addMap(value reflect.Value, depth int) error {
	if value.Len() == 0 {
		return c.add(2)
	}
	if err := c.add(1); err != nil {
		return err
	}
	iterator := value.MapRange()
	first := true
	for iterator.Next() {
		if first {
			first = false
			if err := c.add(1); err != nil {
				return err
			}
		} else if err := c.add(2); err != nil {
			return err
		}
		if err := c.addIndent(depth + 1); err != nil {
			return err
		}
		if err := c.addString(iterator.Key().String()); err != nil {
			return err
		}
		if err := c.add(2); err != nil {
			return err
		}
		if err := c.addValue(iterator.Value(), depth+1); err != nil {
			return err
		}
	}
	if err := c.add(1); err != nil {
		return err
	}
	if err := c.addIndent(depth); err != nil {
		return err
	}
	return c.add(1)
}

func (c *sqliteBundleJSONSizeCounter) addStruct(value reflect.Value, depth int) error {
	if err := c.add(1); err != nil {
		return err
	}
	first := true
	typ := value.Type()
	for index := 0; index < value.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}
		if field.Anonymous {
			return &json.UnsupportedTypeError{Type: typ}
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		for _, option := range strings.Split(options, ",") {
			if option != "" && option != "omitempty" {
				return &json.UnsupportedTypeError{Type: typ}
			}
		}
		if name == "" {
			name = field.Name
		}
		fieldValue := value.Field(index)
		if strings.Contains(","+options+",", ",omitempty,") && isEmptyJSONValue(fieldValue) {
			continue
		}
		if first {
			first = false
			if err := c.add(1); err != nil {
				return err
			}
		} else if err := c.add(2); err != nil {
			return err
		}
		if err := c.addIndent(depth + 1); err != nil {
			return err
		}
		if err := c.addString(name); err != nil {
			return err
		}
		if err := c.add(2); err != nil {
			return err
		}
		if err := c.addValue(fieldValue, depth+1); err != nil {
			return err
		}
	}
	if first {
		return c.add(1)
	}
	if err := c.add(1); err != nil {
		return err
	}
	if err := c.addIndent(depth); err != nil {
		return err
	}
	return c.add(1)
}

func (c *sqliteBundleJSONSizeCounter) enter(value reflect.Value) error {
	visit := sqliteBundleJSONVisit{
		kind: value.Kind(),
		typ:  value.Type(),
		ptr:  value.Pointer(),
	}
	if _, exists := c.seen[visit]; exists {
		return &json.UnsupportedValueError{
			Value: value,
			Str:   "encountered a cycle in sqlite bundle manifest",
		}
	}
	c.seen[visit] = struct{}{}
	return nil
}

func (c *sqliteBundleJSONSizeCounter) leave(value reflect.Value) {
	delete(c.seen, sqliteBundleJSONVisit{
		kind: value.Kind(),
		typ:  value.Type(),
		ptr:  value.Pointer(),
	})
}

func isEmptyJSONValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Interface, reflect.Pointer:
		return value.IsZero()
	default:
		return false
	}
}

func sqliteBundleJSONFloat(value float64, bits int) (string, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return "", &json.UnsupportedValueError{
			Value: reflect.ValueOf(value),
			Str:   strconv.FormatFloat(value, 'g', -1, bits),
		}
	}
	format := byte('f')
	absolute := math.Abs(value)
	if absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
		format = 'e'
	}
	encoded := strconv.AppendFloat(nil, value, format, -1, bits)
	if format == 'e' {
		for index := 0; index+3 < len(encoded); index++ {
			if encoded[index] == 'e' &&
				(encoded[index+1] == '-' || encoded[index+1] == '+') &&
				encoded[index+2] == '0' {
				encoded = append(encoded[:index+2], encoded[index+3:]...)
				break
			}
		}
	}
	return string(encoded), nil
}

func validJSONNumber(value string) bool {
	if value == "" {
		return false
	}
	index := 0
	if value[index] == '-' {
		index++
		if index == len(value) {
			return false
		}
	}
	if value[index] == '0' {
		index++
	} else {
		if value[index] < '1' || value[index] > '9' {
			return false
		}
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
	}
	if index < len(value) && value[index] == '.' {
		index++
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		index++
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			index++
		}
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	return index == len(value)
}
