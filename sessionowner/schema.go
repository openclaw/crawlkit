package sessionowner

import (
	"embed"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed schema/v1/*.schema.json schema/v1/manifest.json
var schemas embed.FS

const schemaPrefix = "urn:openclaw:sessionowner:abi-v1:"

// SchemaStore is an offline, fail-closed subset compiler for this frozen dialect.
// Unsupported keywords are errors, not ignored extensions. Provider schema IDs
// are resolved by the trusted registry, never from a request or the network.
type SchemaStore struct{ docs map[string]any }

func NewSchemaStore() (*SchemaStore, error) {
	s := &SchemaStore{docs: map[string]any{}}
	for _, name := range []string{"request", "response", "cancel", "adapter", "registry-policy"} {
		b, err := schemas.ReadFile("schema/v1/" + name + ".schema.json")
		if err != nil {
			return nil, err
		}
		v, err := DecodeJSON(b, HardFrameLimit)
		if err != nil {
			return nil, err
		}
		s.docs[schemaPrefix+name] = v
	}
	for _, v := range s.docs {
		if err := s.compile(v, false, 0); err != nil {
			return nil, err
		}
	}
	return s, nil
}

var keywords = map[string]bool{"$schema": true, "$id": true, "$defs": true, "$comment": true, "type": true, "properties": true, "required": true, "additionalProperties": true, "oneOf": true, "anyOf": true, "allOf": true, "not": true, "if": true, "then": true, "else": true, "enum": true, "const": true, "minLength": true, "maxLength": true, "pattern": true, "format": true, "minimum": true, "maximum": true, "minItems": true, "maxItems": true, "uniqueItems": true, "items": true, "minProperties": true, "maxProperties": true, "$ref": true}

func (s *SchemaStore) Register(id string, b []byte) error {
	if !validID(id) || strings.Contains(id, "/") {
		return fail(ProtocolMismatch, "schema_id")
	}
	if _, ok := s.docs[id]; ok {
		return fail(ProtocolMismatch, "schema_duplicate")
	}
	v, err := DecodeJSON(b, HardFrameLimit)
	if err != nil {
		return err
	}
	if err = s.compile(v, false, 0); err != nil {
		return err
	}
	if err = s.qualifyProvider(v); err != nil {
		return err
	}
	s.docs[id] = v
	return nil
}
func (s *SchemaStore) compile(v any, closed bool, depth int) error {
	if depth > MaxDepth {
		return fail(ProtocolMismatch, "schema_depth")
	}
	if _, ok := v.(bool); ok {
		return nil
	}
	m, ok := object(v)
	if !ok {
		return fail(ProtocolMismatch, "schema_type")
	}
	for k := range m {
		if !keywords[k] {
			return fail(ProtocolMismatch, "schema_keyword")
		}
	}
	if !validSchemaShape(m) {
		return fail(ProtocolMismatch, "schema_shape")
	}
	if r, ok := m["$ref"].(string); ok {
		target, err := s.resolve(r)
		if err != nil {
			return err
		}
		if closed {
			if err = s.compile(target, true, depth+1); err != nil {
				return err
			}
		}
	}
	if p, ok := m["pattern"].(string); ok {
		if len(p) > 512 {
			return fail(ProtocolMismatch, "pattern")
		}
		if _, e := regexp.Compile(p); e != nil {
			return fail(ProtocolMismatch, "pattern")
		}
	}
	if f, ok := m["format"].(string); ok && f != "date-time" {
		return fail(ProtocolMismatch, "format")
	}
	if closed && (text(m, "type") == "object" || m["properties"] != nil) {
		if _, has := m["additionalProperties"]; !has {
			return fail(ProtocolMismatch, "open_schema")
		}
		if b, ok := m["additionalProperties"].(bool); ok && b {
			return fail(ProtocolMismatch, "open_schema")
		}
	}
	for _, k := range []string{"properties", "$defs"} {
		if v, has := m[k]; has {
			props, ok := object(v)
			if !ok {
				return fail(ProtocolMismatch, "schema_properties")
			}
			for _, sub := range props {
				if err := s.compile(sub, closed, depth+1); err != nil {
					return err
				}
			}
		}
	}
	for _, k := range []string{"oneOf", "anyOf", "allOf"} {
		if v, has := m[k]; has {
			a, ok := v.([]any)
			if !ok || len(a) == 0 {
				return fail(ProtocolMismatch, "schema_combination")
			}
			for _, sub := range a {
				if err := s.compile(sub, closed, depth+1); err != nil {
					return err
				}
			}
		}
	}
	for _, k := range []string{"items", "not", "if", "then", "else", "additionalProperties"} {
		if v, ok := m[k]; ok {
			if err := s.compile(v, closed, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *SchemaStore) resolve(id string) (any, error) {
	a := strings.SplitN(id, "#", 2)
	v, ok := s.docs[a[0]]
	if !ok {
		return nil, fail(ProtocolMismatch, "schema_reference")
	}
	if len(a) == 2 {
		if !strings.HasPrefix(a[1], "/") {
			return nil, fail(ProtocolMismatch, "schema_reference")
		}
		for _, p := range strings.Split(a[1][1:], "/") {
			m, ok := object(v)
			if !ok {
				return nil, fail(ProtocolMismatch, "schema_reference")
			}
			v, ok = m[strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")]
			if !ok {
				return nil, fail(ProtocolMismatch, "schema_reference")
			}
		}
	}
	return v, nil
}
func (s *SchemaStore) Validate(id string, v any) error {
	doc, e := s.resolve(id)
	if e != nil {
		return e
	}
	if !s.match(doc, v, 0) {
		return fail(ProtocolMismatch, "schema_invalid")
	}
	return nil
}
func (s *SchemaStore) match(schema, v any, depth int) bool {
	if depth > MaxDepth*4 {
		return false
	}
	if b, ok := schema.(bool); ok {
		return b
	}
	m, ok := object(schema)
	if !ok {
		return false
	}
	if r, ok := m["$ref"].(string); ok {
		d, e := s.resolve(r)
		if e != nil || !s.match(d, v, depth+1) {
			return false
		}
	}
	if c, ok := m["const"]; ok && !equalJSON(c, v) {
		return false
	}
	if e, ok := m["enum"].([]any); ok {
		found := false
		for _, c := range e {
			found = found || equalJSON(c, v)
		}
		if !found {
			return false
		}
	}
	for _, kind := range []string{"oneOf", "anyOf", "allOf"} {
		if a, ok := m[kind].([]any); ok {
			n := 0
			for _, x := range a {
				if s.match(x, v, depth+1) {
					n++
				}
			}
			if kind == "oneOf" && n != 1 || kind == "anyOf" && n == 0 || kind == "allOf" && n != len(a) {
				return false
			}
		}
	}
	if n, ok := m["not"]; ok && s.match(n, v, depth+1) {
		return false
	}
	if cond, ok := m["if"]; ok {
		key := "else"
		if s.match(cond, v, depth+1) {
			key = "then"
		}
		if x, ok := m[key]; ok && !s.match(x, v, depth+1) {
			return false
		}
	}
	if typ, ok := m["type"]; ok {
		types, ok := typ.([]any)
		if !ok {
			types = []any{typ}
		}
		match := false
		for _, t := range types {
			match = match || matchesType(t, v)
		}
		if !match {
			return false
		}
	}
	if str, ok := v.(string); ok {
		n := float64(utf8.RuneCountInString(str))
		if !bounds(m, "minLength", "maxLength", n) {
			return false
		}
		if p, ok := m["pattern"].(string); ok {
			re, e := regexp.Compile(p)
			if e != nil || !re.MatchString(str) {
				return false
			}
		}
		if m["format"] == "date-time" && !ValidTimestamp(str) {
			return false
		}
		// Both dialect revisions and sequence counters use this exact pattern.
		if m["pattern"] == "^(0|[1-9][0-9]{0,19})$" {
			if _, err := strconv.ParseUint(str, 10, 64); err != nil {
				return false
			}
		}
	}
	if _, ok := v.(json.Number); ok {
		if !bounds(m, "minimum", "maximum", num(v)) {
			return false
		}
	}
	if a, ok := v.([]any); ok {
		if !bounds(m, "minItems", "maxItems", float64(len(a))) {
			return false
		}
		if sub, ok := m["items"]; ok {
			for _, x := range a {
				if !s.match(sub, x, depth+1) {
					return false
				}
			}
		}
		if m["uniqueItems"] == true {
			seen := map[string]bool{}
			for _, x := range a {
				b, _ := json.Marshal(x)
				if seen[string(b)] {
					return false
				}
				seen[string(b)] = true
			}
		}
	}
	if o, ok := object(v); ok {
		if !bounds(m, "minProperties", "maxProperties", float64(len(o))) {
			return false
		}
		if req, ok := m["required"].([]any); ok {
			for _, k := range req {
				if _, ok := o[k.(string)]; !ok {
					return false
				}
			}
		}
		props, _ := object(m["properties"])
		for k, x := range o {
			if p, ok := props[k]; ok {
				if !s.match(p, x, depth+1) {
					return false
				}
			} else if a, ok := m["additionalProperties"]; ok {
				if !s.match(a, x, depth+1) {
					return false
				}
			}
		}
	}
	return true
}
func matchesType(t, v any) bool {
	switch t {
	case "object":
		_, ok := object(v)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		_, ok := v.(json.Number)
		return ok
	case "integer":
		_, ok := v.(json.Number)
		return ok && math.Trunc(num(v)) == num(v)
	}
	return false
}
func bounds(m map[string]any, lo, hi string, n float64) bool {
	if math.IsNaN(n) {
		return false
	}
	if v, ok := m[lo]; ok && n < num(v) {
		return false
	}
	if v, ok := m[hi]; ok && n > num(v) {
		return false
	}
	return true
}
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func ValidTimestamp(s string) bool {
	t, e := time.Parse("2006-01-02T15:04:05.000Z", s)
	return e == nil && t.Year() >= 1 && t.Format("2006-01-02T15:04:05.000Z") == s
}
func validID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for i, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i == 0 || !strings.ContainsRune("_.:-", rune(c)) {
			return false
		}
	}
	return true
}

func validSchemaShape(m map[string]any) bool {
	for _, k := range []string{"$schema", "$id", "$comment", "$ref", "pattern", "format"} {
		if v, ok := m[k]; ok {
			if _, ok := v.(string); !ok {
				return false
			}
		}
	}
	if v, ok := m["required"]; ok {
		a, ok := v.([]any)
		if !ok {
			return false
		}
		seen := map[string]bool{}
		for _, x := range a {
			k, ok := x.(string)
			if !ok || seen[k] {
				return false
			}
			seen[k] = true
		}
	}
	if v, ok := m["enum"]; ok {
		a, ok := v.([]any)
		if !ok || len(a) == 0 {
			return false
		}
	}
	if v, ok := m["type"]; ok {
		a, ok := v.([]any)
		if !ok {
			a = []any{v}
		}
		if len(a) == 0 {
			return false
		}
		for _, x := range a {
			t, ok := x.(string)
			if !ok || !(t == "object" || t == "array" || t == "string" || t == "boolean" || t == "null" || t == "number" || t == "integer") {
				return false
			}
		}
	}
	for _, k := range []string{"minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties", "minimum", "maximum"} {
		if v, ok := m[k]; ok {
			if _, ok := v.(json.Number); !ok {
				return false
			}
			n := num(v)
			if math.IsNaN(n) {
				return false
			}
			if k != "minimum" && k != "maximum" && (n < 0 || math.Trunc(n) != n) {
				return false
			}
		}
	}
	if v, ok := m["uniqueItems"]; ok {
		if _, ok := v.(bool); !ok {
			return false
		}
	}
	return true
}
