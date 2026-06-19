package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

func FTS5Phrase(value string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(value), `"`, `""`) + `"`
}

// FTS5Terms converts whitespace-delimited user input into quoted FTS5 terms.
// Operator may be empty (implicit AND), AND, or OR.
func FTS5Terms(value, operator string) (string, error) {
	operator = strings.ToUpper(strings.TrimSpace(operator))
	if operator != "" && operator != "AND" && operator != "OR" {
		return "", fmt.Errorf("unsupported FTS5 operator %q", operator)
	}
	terms := strings.Fields(value)
	if len(terms) == 0 {
		return FTS5Phrase(""), nil
	}
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, FTS5Phrase(term))
	}
	separator := " "
	if operator != "" {
		separator = " " + operator + " "
	}
	return strings.Join(quoted, separator), nil
}

// FTS5TokenQuery converts arbitrary user input into quoted FTS5 tokens joined
// by implicit AND, discarding punctuation that could be parsed as operators.
func FTS5TokenQuery(value string) string {
	terms := make([]string, 0)
	var token strings.Builder
	flush := func() {
		if token.Len() == 0 {
			return
		}
		terms = append(terms, FTS5Phrase(token.String()))
		token.Reset()
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			token.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return strings.Join(terms, " ")
}

type contextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func OptimizeFTS5(ctx context.Context, db contextExecer, table string) error {
	table = strings.TrimSpace(table)
	if table == "" || strings.ContainsAny(table, "\"\x00") {
		return fmt.Errorf("invalid FTS5 table %q", table)
	}
	quoted := QuoteIdent(table)
	if _, err := db.ExecContext(ctx, "insert into "+quoted+"("+quoted+") values('optimize')"); err != nil {
		return fmt.Errorf("optimize FTS5 table %s: %w", table, err)
	}
	return nil
}
