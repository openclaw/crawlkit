package sessionowner

// Qualification is separate from schema execution. It checks each accepting
// branch, including unions, references, conditionals and array/map values.
// Bounded branch expansion permits ordinary closed allOf/nullable schemas without
// pretending to solve arbitrary JSON Schema satisfiability.

func (s *SchemaStore) qualifyProvider(v any) error {
	q := &closureQualifier{store: s, remaining: 50000}
	err := q.qualifyConjunction([]any{v}, 0)
	if q.exhausted {
		return fail(ProtocolMismatch, "closure_budget")
	}
	return err
}

type closureQualifier struct {
	store     *SchemaStore
	remaining int
	exhausted bool
}

func (q *closureQualifier) spend() bool {
	q.remaining--
	if q.remaining < 0 {
		q.exhausted = true
		return false
	}
	return true
}
func (q *closureQualifier) resolve(id string) (any, error) { return q.store.resolve(id) }

func (s *closureQualifier) alternatives(v any, depth int) ([][]any, error) {
	if !s.spend() || depth > MaxDepth {
		return nil, fail(ProtocolMismatch, "closure_depth")
	}
	if _, ok := v.(bool); ok {
		return [][]any{{v}}, nil
	}
	m, ok := object(v)
	if !ok {
		return nil, fail(ProtocolMismatch, "schema_type")
	}
	base := map[string]any{}
	for k, x := range m {
		switch k {
		case "$ref", "allOf", "anyOf", "oneOf", "if", "then", "else":
		default:
			base[k] = x
		}
	}
	result := [][]any{{base}}
	join := func(other [][]any) error {
		if len(result)*len(other) > 64 {
			return fail(ProtocolMismatch, "closure_branches")
		}
		next := [][]any{}
		for _, a := range result {
			for _, b := range other {
				c := append(append([]any{}, a...), b...)
				next = append(next, c)
			}
		}
		result = next
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		target, e := s.resolve(ref)
		if e != nil {
			return nil, e
		}
		a, e := s.alternatives(target, depth+1)
		if e != nil {
			return nil, e
		}
		if e = join(a); e != nil {
			return nil, e
		}
	}
	if a, ok := m["allOf"].([]any); ok {
		for _, x := range a {
			branches, e := s.alternatives(x, depth+1)
			if e != nil {
				return nil, e
			}
			if e = join(branches); e != nil {
				return nil, e
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if a, ok := m[key].([]any); ok {
			branches := [][]any{}
			for _, x := range a {
				b, e := s.alternatives(x, depth+1)
				if e != nil {
					return nil, e
				}
				branches = append(branches, b...)
				if len(branches) > 64 {
					return nil, fail(ProtocolMismatch, "closure_branches")
				}
			}
			if e := join(branches); e != nil {
				return nil, e
			}
		}
	}
	if cond, ok := m["if"]; ok {
		thenSchema, has := m["then"]
		if !has {
			thenSchema = true
		}
		elseSchema, has := m["else"]
		if !has {
			elseSchema = true
		}
		branches := [][]any{}
		for _, pair := range [][]any{{cond, thenSchema}, {map[string]any{"not": cond}, elseSchema}} {
			a, e := s.alternatives(map[string]any{"allOf": pair}, depth+1)
			if e != nil {
				return nil, e
			}
			branches = append(branches, a...)
		}
		if e := join(branches); e != nil {
			return nil, e
		}
	}
	return result, nil
}

// possibleKind/allKind are conservative. Unsupported negative constraints never
// manufacture a closure proof; branch count/depth bounds fail explicitly.
func (s *closureQualifier) possibleKind(v any, kind string, depth int) bool {
	if !s.spend() || depth > MaxDepth {
		return true
	}
	if b, ok := v.(bool); ok {
		return b
	}
	m, _ := object(v)
	if typ, ok := m["type"]; ok {
		a, ok := typ.([]any)
		if !ok {
			a = []any{typ}
		}
		found := false
		for _, t := range a {
			if t == kind {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if c, ok := m["const"]; ok && !matchesType(kind, c) {
		return false
	}
	if a, ok := m["enum"].([]any); ok {
		found := false
		for _, x := range a {
			found = found || matchesType(kind, x)
		}
		if !found {
			return false
		}
	}
	if n, ok := m["not"]; ok && s.allKind(n, kind, depth+1) {
		return false
	}
	return true
}
func (s *closureQualifier) allKind(v any, kind string, depth int) bool {
	if !s.spend() || depth > MaxDepth {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	m, ok := object(v)
	if !ok || !s.possibleKind(v, kind, depth+1) {
		return false
	}
	if ref, ok := m["$ref"].(string); ok {
		target, e := s.resolve(ref)
		if e != nil || !s.allKind(target, kind, depth+1) {
			return false
		}
	}
	if _, ok = m["const"]; ok {
		return false
	}
	if _, ok = m["enum"]; ok {
		return false
	}
	if n, ok := m["not"]; ok && s.possibleKind(n, kind, depth+1) {
		return false
	}
	keys := []string{"properties", "required", "additionalProperties", "minProperties", "maxProperties"}
	if kind == "array" {
		keys = []string{"items", "minItems", "maxItems", "uniqueItems"}
	}
	for _, k := range keys {
		if _, ok = m[k]; ok {
			return false
		}
	}
	if a, ok := m["allOf"].([]any); ok {
		for _, x := range a {
			if !s.allKind(x, kind, depth+1) {
				return false
			}
		}
	}
	if a, ok := m["anyOf"].([]any); ok {
		found := false
		for _, x := range a {
			found = found || s.allKind(x, kind, depth+1)
		}
		if !found {
			return false
		}
	}
	if a, ok := m["oneOf"].([]any); ok {
		sole := -1
		for idx, x := range a {
			if s.allKind(x, kind, depth+1) {
				if sole != -1 {
					return false
				}
				sole = idx
			}
		}
		if sole < 0 {
			return false
		}
		for idx, x := range a {
			if idx != sole && s.possibleKind(x, kind, depth+1) {
				return false
			}
		}
	}
	if cond, ok := m["if"]; ok {
		yes, has := m["then"]
		if !has {
			yes = true
		}
		no, has := m["else"]
		if !has {
			no = true
		}
		if s.allKind(cond, kind, depth+1) {
			return s.allKind(yes, kind, depth+1)
		}
		if !s.possibleKind(cond, kind, depth+1) {
			return s.allKind(no, kind, depth+1)
		}
		if !s.allKind(yes, kind, depth+1) || !s.allKind(no, kind, depth+1) {
			return false
		}
	}
	return true
}
func (s *closureQualifier) qualifyConjunction(values []any, depth int) error {
	if !s.spend() || depth > MaxDepth {
		return fail(ProtocolMismatch, "closure_depth")
	}
	branches, e := s.alternatives(map[string]any{"allOf": values}, 0)
	if e != nil {
		return e
	}
	for _, branch := range branches {
		finite := false
		for _, x := range branch {
			if m, ok := object(x); ok {
				if _, ok := m["const"]; ok {
					finite = true
				}
				if _, ok := m["enum"]; ok {
					finite = true
				}
			}
		}
		// A finite whole-value constraint permits no caller-created keys or values.
		if finite {
			continue
		}
		objectPossible, arrayPossible := true, true
		for _, x := range branch {
			objectPossible = objectPossible && s.possibleKind(x, "object", 0)
			arrayPossible = arrayPossible && s.possibleKind(x, "array", 0)
		}
		if objectPossible {
			closed, empty := false, false
			names := map[string]bool{}
			for _, x := range branch {
				m, ok := object(x)
				if !ok {
					continue
				}
				if v, ok := m["maxProperties"]; ok && num(v) == 0 {
					closed = true
					empty = true
				}
				if ap, ok := m["additionalProperties"]; ok {
					if b, ok := ap.(bool); !ok || !b {
						closed = true
					}
				}
				if props, ok := object(m["properties"]); ok {
					for name := range props {
						names[name] = true
					}
				}
			}
			if !closed {
				return fail(ProtocolMismatch, "open_schema")
			}
			if !empty {
				for name := range names {
					schemas := []any{}
					for _, x := range branch {
						m, ok := object(x)
						if !ok {
							continue
						}
						props, _ := object(m["properties"])
						if sub, ok := props[name]; ok {
							schemas = append(schemas, sub)
						} else if ap, ok := m["additionalProperties"]; ok {
							schemas = append(schemas, ap)
						}
					}
					if e = s.qualifyConjunction(schemas, depth+1); e != nil {
						return e
					}
				}
				maps := []any{}
				for _, x := range branch {
					if m, ok := object(x); ok {
						if ap, ok := m["additionalProperties"]; ok {
							maps = append(maps, ap)
						}
					}
				}
				if e = s.qualifyConjunction(maps, depth+1); e != nil {
					return e
				}
			}
		}
		if arrayPossible {
			empty := false
			items := []any{}
			for _, x := range branch {
				m, ok := object(x)
				if !ok {
					continue
				}
				if v, ok := m["maxItems"]; ok && num(v) == 0 {
					empty = true
				}
				if sub, ok := m["items"]; ok {
					items = append(items, sub)
				}
			}
			if !empty {
				if len(items) == 0 {
					return fail(ProtocolMismatch, "open_array_schema")
				}
				if e = s.qualifyConjunction(items, depth+1); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
