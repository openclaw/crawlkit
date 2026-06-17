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
