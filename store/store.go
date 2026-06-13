package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

const DefaultBusyTimeoutMillis = 5000

type Options struct {
	Path          string
	Schema        string
	SchemaVersion int
	MaxOpenConns  int
	MaxIdleConns  int
}

type Store struct {
	db   *sql.DB
	path string
}

type QueryResult struct {
	Columns []string         `json:"columns"`
	Rows    [][]any          `json:"rows"`
	Values  []map[string]any `json:"values,omitempty"`
}

func Open(ctx context.Context, opts Options) (*Store, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	if err := ensureDBFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", writeDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	configurePool(db, opts)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := tightenDBFilePerms(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db, path: path}
	if opts.Schema != "" {
		if _, err := db.ExecContext(ctx, opts.Schema); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply schema: %w", err)
		}
	}
	if opts.SchemaVersion > 0 {
		if err := store.EnsureSchemaVersion(ctx, opts.SchemaVersion); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("stat sqlite db: %w", err)
		}
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite readonly: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite readonly: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *Store) EnsureSchemaVersion(ctx context.Context, version int) error {
	if version <= 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `create table if not exists schema_migrations(version integer not null)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current > version {
		return fmt.Errorf("database schema version %d is newer than supported version %d", current, version)
	}
	if current == version {
		return nil
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `delete from schema_migrations`); err != nil {
			return fmt.Errorf("clear schema version: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `insert into schema_migrations(version) values(?)`, version); err != nil {
			return fmt.Errorf("write schema version: %w", err)
		}
		return nil
	})
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'schema_migrations'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var version int
	err := s.db.QueryRowContext(ctx, `select coalesce(max(version), 0) from schema_migrations`).Scan(&version)
	return version, err
}

func (s *Store) Query(ctx context.Context, query string, args ...any) (QueryResult, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return QueryResult{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Columns: cols}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return QueryResult{}, err
		}
		row := make([]any, len(cols))
		named := make(map[string]any, len(cols))
		for i, value := range values {
			row[i] = normalizeValue(value)
			named[cols[i]] = row[i]
		}
		result.Rows = append(result.Rows, row)
		result.Values = append(result.Values, named)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	return result, nil
}

func QuoteIdent(name string) string {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\"\x00") {
		panic(fmt.Sprintf("unsafe sqlite identifier: %q", name))
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func writeDSN(path string) string {
	return dsn(path, "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(268435456)&_pragma=busy_timeout(5000)")
}

func readOnlyDSN(path string) string {
	return dsn(path, "mode=ro&_pragma=query_only(1)&_pragma=foreign_keys(1)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(268435456)&_pragma=busy_timeout(5000)")
}

func dsn(path, pragmas string) string {
	if path == ":memory:" {
		return "file::memory:?cache=shared&" + pragmas
	}
	if strings.HasPrefix(path, "file:") {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return path + sep + pragmas
	}
	u := url.URL{Scheme: "file", Path: path}
	return u.String() + "?" + pragmas
}

func ensureDBFile(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create sqlite dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat sqlite db: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create sqlite db: %w", err)
	}
	if file != nil {
		if err := file.Close(); err != nil {
			return fmt.Errorf("close sqlite db: %w", err)
		}
	}
	return nil
}

func tightenDBFilePerms(path string) error {
	if runtime.GOOS == "windows" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod sqlite db: %w", err)
	}
	return nil
}

func configurePool(db *sql.DB, opts Options) {
	maxOpen := opts.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 1
	}
	maxIdle := opts.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 1
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
}

func normalizeValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}
