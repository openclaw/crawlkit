package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsNewerVersionBeforeSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	st, err := Open(ctx, Options{Path: path, SchemaVersion: 9, Schema: `
		create table retained(value text);
		insert into retained values('original');
	`})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	rejected, err := Open(ctx, Options{Path: path, SchemaVersion: 8, Schema: `drop table retained;`})
	if rejected != nil {
		rejected.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Open error = %v", err)
	}
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var value string
	if err := ro.DB().QueryRowContext(ctx, `select value from retained`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "original" {
		t.Fatalf("retained value = %q", value)
	}
}

func TestOpenSchemaFailureDoesNotAdvanceVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	st, err := Open(ctx, Options{Path: path, SchemaVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(ctx, Options{Path: path, SchemaVersion: 3, Schema: `invalid SQL`}); err == nil {
		t.Fatal("expected schema failure")
	}
	st, err = Open(ctx, Options{Path: path, SchemaVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	version, err := st.SchemaVersion(ctx)
	if err != nil || version != 2 {
		t.Fatalf("version = %d, error = %v", version, err)
	}
}

func TestWithTxRollsBackPanicAndReleasesConnection(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "archive.db"), Schema: `create table things(value text)`})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sentinel := errors.New("callback panic")
	func() {
		defer func() {
			if got := recover(); got != sentinel {
				t.Errorf("panic = %v, want original sentinel", got)
			}
		}()
		_ = st.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `insert into things values('uncommitted')`); err != nil {
				t.Fatal(err)
			}
			panic(sentinel)
		})
	}()
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var count int
	if err := st.DB().QueryRowContext(queryCtx, `select count(*) from things`).Scan(&count); err != nil {
		t.Fatalf("connection not reusable: %v", err)
	}
	if count != 0 {
		t.Fatalf("panic committed %d rows", count)
	}
	if err := st.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into things values('error')`); err != nil {
			return err
		}
		return sentinel
	}); err != sentinel {
		t.Fatalf("callback error = %v", err)
	}
	if err := st.DB().QueryRowContext(ctx, `select count(*) from things`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("callback error count = %d, error = %v", count, err)
	}
}

func TestAnonymousMemoryStoresAreIndependent(t *testing.T) {
	ctx := context.Background()
	a, err := Open(ctx, Options{Path: ":memory:", Schema: `create table things(value text); insert into things values('a');`, MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, Options{Path: ":memory:", Schema: `create table things(value text); insert into things values('b');`})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, tc := range []struct {
		store *Store
		want  string
	}{{a, "a"}, {b, "b"}} {
		if tc.store.Path() != ":memory:" {
			t.Fatalf("Path = %q", tc.store.Path())
		}
		var value string
		if err := tc.store.DB().QueryRowContext(ctx, `select value from things`).Scan(&value); err != nil || value != tc.want {
			t.Fatalf("value = %q, error = %v, want %q", value, err, tc.want)
		}
	}
	held, err := a.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	var value string
	if err := a.DB().QueryRowContext(ctx, `select value from things`).Scan(&value); err != nil || value != "a" {
		t.Fatalf("second pooled connection value = %q, error = %v", value, err)
	}
}

func TestExplicitNamedMemoryStoresRemainShared(t *testing.T) {
	ctx := context.Background()
	path := "file:explicit-store-test?mode=memory&cache=shared"
	a, err := Open(ctx, Options{Path: path, Schema: `create table things(value text); insert into things values('shared');`})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var value string
	if err := b.DB().QueryRowContext(ctx, `select value from things`).Scan(&value); err != nil || value != "shared" {
		t.Fatalf("shared value = %q, error = %v", value, err)
	}
}

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

func TestOpenHonorsCallerSQLiteURIParameters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	uri := dsn(path, "_journal_mode=delete")
	st, err := Open(ctx, Options{Path: uri})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var journalMode string
	if err := st.DB().QueryRowContext(ctx, `pragma journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "delete" {
		t.Fatalf("journal mode = %q, want caller-provided delete mode", journalMode)
	}
}

func TestOpenRejectsInvalidCallerSQLiteURIParameters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	uri := dsn(path, "_busy_timeout=5s")
	st, err := Open(ctx, Options{Path: uri})
	if st != nil {
		_ = st.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "_busy_timeout") {
		t.Fatalf("Open error = %v, want invalid _busy_timeout error", err)
	}
}

func TestOpenReadOnlyPreservesModeWithCallerSQLiteURIParameters(t *testing.T) {
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

	uri := dsn(path, "_query_only=0")
	ro, err := OpenReadOnly(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()

	var queryOnly int
	if err := ro.DB().QueryRowContext(ctx, `pragma query_only`).Scan(&queryOnly); err != nil {
		t.Fatal(err)
	}
	if queryOnly != 0 {
		t.Fatalf("query_only = %d, want caller-provided 0", queryOnly)
	}
	if _, err := ro.DB().ExecContext(ctx, `insert into things(id) values('x')`); err == nil {
		t.Fatal("mode=ro must reject writes even when the caller disables query_only")
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
