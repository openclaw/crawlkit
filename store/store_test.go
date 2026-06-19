package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenAppliesSchemaPragmasAndPermissions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	st, err := Open(ctx, Options{
		Path:          path,
		Schema:        `create table things(id text primary key, value text not null);`,
		SchemaVersion: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Path() != path {
		t.Fatalf("path = %q, want %q", st.Path(), path)
	}

	var journalMode string
	if err := st.DB().QueryRowContext(ctx, `pragma journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q", journalMode)
	}
	version, err := st.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("schema version = %d", version)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		got := info.Mode().Perm()
		t.Fatalf("mode = %o", got)
	}
}

func TestWithTxAndQuery(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, Options{
		Path:   filepath.Join(t.TempDir(), "archive.db"),
		Schema: `create table things(id text primary key, value text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `insert into things(id, value) values('a', 'one')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result, err := st.Query(ctx, `select id, value from things`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Values[0]["value"] != "one" {
		t.Fatalf("unexpected query result: %+v", result)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	st, err := Open(ctx, Options{
		Path:   path,
		Schema: `create table things(id text primary key);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.DB().ExecContext(ctx, `insert into things(id) values('x')`); err == nil {
		t.Fatal("expected readonly write to fail")
	}
}

func TestOpenEscapesURIReservedPathCharacters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	name := "archive?tenant=a#frag.db"
	if runtime.GOOS == "windows" {
		name = "archive#frag.db"
	}
	path := filepath.Join(dir, name)
	st, err := Open(ctx, Options{
		Path:   path,
		Schema: `create table things(id text primary key, value text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `insert into things(id, value) values('a', 'one')`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("literal database path missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "archive")); !os.IsNotExist(err) {
		t.Fatalf("unexpected truncated database stat err = %v", err)
	}

	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var value string
	if err := ro.DB().QueryRowContext(ctx, `select value from things where id = 'a'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "one" {
		t.Fatalf("value = %q", value)
	}
}

func TestDSNUsesAbsoluteWindowsFileURI(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific file URI")
	}
	got := dsn(`C:\Users\runner\archive.db`, "_pragma=foreign_keys(1)")
	if !strings.HasPrefix(got, "file:///C:/Users/runner/archive.db?") {
		t.Fatalf("dsn = %q", got)
	}
}

func TestFTS5Helpers(t *testing.T) {
	query, err := FTS5Terms(`hello "quoted" world`, "AND")
	if err != nil {
		t.Fatal(err)
	}
	if query != `"hello" AND """quoted""" AND "world"` {
		t.Fatalf("query = %q", query)
	}
	if _, err := FTS5Terms("hello", "NEAR"); err == nil {
		t.Fatal("unsupported operator should fail")
	}
	if query := FTS5TokenQuery(`scope-upgrade OR café_2`); query != `"scope" "upgrade" "OR" "café_2"` {
		t.Fatalf("token query = %q", query)
	}
	if query := FTS5TokenQuery("e\u0301clair"); query != "\"e\u0301clair\"" {
		t.Fatalf("decomposed Unicode token query = %q", query)
	}
	for _, input := range []string{"", `-- ""`, `*:^()`} {
		if query := FTS5TokenQuery(input); query != "" {
			t.Fatalf("empty/punctuation-only token query = %q", query)
		}
	}

	ctx := context.Background()
	st, err := Open(ctx, Options{
		Path:   filepath.Join(t.TempDir(), "fts.db"),
		Schema: `create virtual table docs using fts5(id unindexed, body);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	docs := []struct {
		id   string
		body string
	}{
		{id: "plain", body: "scope upgrade"},
		{id: "spaced", body: "scope filler upgrade"},
		{id: "operator", body: "foo OR bar"},
		{id: "unicode", body: "café_2 東京 мир"},
		{id: "decomposed", body: "e\u0301clair"},
	}
	for _, doc := range docs {
		if _, err := st.DB().ExecContext(ctx, `insert into docs(id, body) values (?, ?)`, doc.id, doc.body); err != nil {
			t.Fatal(err)
		}
	}
	queries := []struct {
		input string
		want  string
	}{
		{input: `scope-upgrade`, want: "plain,spaced"},
		{input: `foo" OR bar*`, want: "operator"},
		{input: `café_2 東京 мир`, want: "unicode"},
		{input: "e\u0301clair", want: "decomposed"},
	}
	for _, tt := range queries {
		var got string
		query := FTS5TokenQuery(tt.input)
		err := st.DB().QueryRowContext(ctx, `
			select coalesce(group_concat(id, ','), '')
			from (select id from docs where docs match ? order by id)
		`, query).Scan(&got)
		if err != nil {
			t.Fatalf("query %q: %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("query %q matches %q, want %q", tt.input, got, tt.want)
		}
	}
	if err := OptimizeFTS5(ctx, st.DB(), "docs"); err != nil {
		t.Fatal(err)
	}
	if err := OptimizeFTS5(ctx, st.DB(), `bad"table`); err == nil {
		t.Fatal("unsafe FTS table should fail")
	}
}
