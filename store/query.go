package store

import "strings"

// EscapeLike escapes a literal value for a LIKE expression using backslash as
// the SQL ESCAPE character. Callers must add `ESCAPE '\'` to the expression.
func EscapeLike(value string) string {
	var escaped strings.Builder
	for _, r := range value {
		if r == '\\' || r == '%' || r == '_' {
			escaped.WriteRune('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}
