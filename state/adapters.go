package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const ScopedSchema = `
create table if not exists sync_scoped_state (
  scope text primary key,
  cursor text not null,
  updated_at text not null
);
create index if not exists idx_sync_scoped_state_updated_at on sync_scoped_state(updated_at desc);
`

const CursorSchema = `
create table if not exists sync_cursor_state (
  source text not null,
  entity_type text not null,
  entity_id text not null,
  cursor text not null,
  synced_at text not null,
  primary key (source, entity_type, entity_id)
);
create index if not exists idx_sync_cursor_state_synced_at on sync_cursor_state(synced_at desc);
`

type ScopedStore struct {
	db      execQuerier
	now     func() time.Time
	mapping ScopedMapping
}

type ScopedMapping struct {
	Table     string
	Scope     string
	Cursor    string
	UpdatedAt string
}

type ScopedRecord struct {
	Scope     string    `json:"scope"`
	Cursor    string    `json:"cursor"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CursorStore struct {
	db      execQuerier
	now     func() time.Time
	mapping CursorMapping
}

type CursorMapping struct {
	Table      string
	Source     string
	EntityType string
	EntityID   string
	Cursor     string
	SyncedAt   string
}

type CursorRecord struct {
	Source     string    `json:"source"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	Cursor     string    `json:"cursor"`
	SyncedAt   time.Time `json:"synced_at"`
}

func EnsureScopedSchema(ctx context.Context, db execQuerier) error {
	if _, err := db.ExecContext(ctx, ScopedSchema); err != nil {
		return fmt.Errorf("ensure scoped sync state schema: %w", err)
	}
	return nil
}

func EnsureCursorSchema(ctx context.Context, db execQuerier) error {
	if _, err := db.ExecContext(ctx, CursorSchema); err != nil {
		return fmt.Errorf("ensure cursor sync state schema: %w", err)
	}
	return nil
}

func NewScoped(db execQuerier) *ScopedStore {
	return NewScopedWithClock(db, nil)
}

func NewScopedWithClock(db execQuerier, now func() time.Time) *ScopedStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ScopedStore{db: db, now: now, mapping: defaultScopedMapping()}
}

func NewScopedMapped(db execQuerier, mapping ScopedMapping) (*ScopedStore, error) {
	mapping, err := normalizeScopedMapping(mapping)
	if err != nil {
		return nil, err
	}
	return &ScopedStore{db: db, now: func() time.Time { return time.Now().UTC() }, mapping: mapping}, nil
}

func (s *ScopedStore) Set(ctx context.Context, scope, cursor string) error {
	updatedAt := s.now().UTC()
	m := s.mapping
	query := fmt.Sprintf(`
insert into %s(%s, %s, %s)
values (?, ?, ?)
on conflict(%s) do update set
  %s = excluded.%s,
  %s = excluded.%s
`, quote(m.Table), quote(m.Scope), quote(m.Cursor), quote(m.UpdatedAt), quote(m.Scope), quote(m.Cursor), quote(m.Cursor), quote(m.UpdatedAt), quote(m.UpdatedAt))
	_, err := s.db.ExecContext(ctx, query, scope, cursor, updatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set scoped sync state: %w", err)
	}
	return nil
}

func (s *ScopedStore) Get(ctx context.Context, scope string) (ScopedRecord, bool, error) {
	var rec ScopedRecord
	var updatedAt string
	m := s.mapping
	query := fmt.Sprintf("select %s, %s, %s from %s where %s = ?", quote(m.Scope), quote(m.Cursor), quote(m.UpdatedAt), quote(m.Table), quote(m.Scope))
	err := s.db.QueryRowContext(ctx, query, scope).Scan(&rec.Scope, &rec.Cursor, &updatedAt)
	if err == sql.ErrNoRows {
		return ScopedRecord{}, false, nil
	}
	if err != nil {
		return ScopedRecord{}, false, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return ScopedRecord{}, false, fmt.Errorf("parse scoped sync state updated_at: %w", err)
	}
	rec.UpdatedAt = parsed
	return rec, true, nil
}

func (s *ScopedStore) IsStale(ctx context.Context, scope string, maxAge time.Duration) (bool, error) {
	rec, ok, err := s.Get(ctx, scope)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if maxAge <= 0 {
		return false, nil
	}
	return s.now().UTC().Sub(rec.UpdatedAt) > maxAge, nil
}

func NewCursor(db execQuerier) *CursorStore {
	return NewCursorWithClock(db, nil)
}

func NewCursorWithClock(db execQuerier, now func() time.Time) *CursorStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &CursorStore{db: db, now: now, mapping: defaultCursorMapping()}
}

func NewCursorMapped(db execQuerier, mapping CursorMapping) (*CursorStore, error) {
	mapping, err := normalizeCursorMapping(mapping)
	if err != nil {
		return nil, err
	}
	return &CursorStore{db: db, now: func() time.Time { return time.Now().UTC() }, mapping: mapping}, nil
}

func (s *CursorStore) Set(ctx context.Context, source, entityType, entityID, cursor string) error {
	syncedAt := s.now().UTC()
	m := s.mapping
	query := fmt.Sprintf(`
insert into %s(%s, %s, %s, %s, %s)
values (?, ?, ?, ?, ?)
on conflict(%s, %s, %s) do update set
  %s = excluded.%s,
  %s = excluded.%s
`, quote(m.Table), quote(m.Source), quote(m.EntityType), quote(m.EntityID), quote(m.Cursor), quote(m.SyncedAt), quote(m.Source), quote(m.EntityType), quote(m.EntityID), quote(m.Cursor), quote(m.Cursor), quote(m.SyncedAt), quote(m.SyncedAt))
	_, err := s.db.ExecContext(ctx, query, source, entityType, entityID, cursor, syncedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set cursor sync state: %w", err)
	}
	return nil
}

func (s *CursorStore) Get(ctx context.Context, source, entityType, entityID string) (CursorRecord, bool, error) {
	var rec CursorRecord
	var syncedAt string
	m := s.mapping
	query := fmt.Sprintf("select %s, %s, %s, %s, %s from %s where %s = ? and %s = ? and %s = ?", quote(m.Source), quote(m.EntityType), quote(m.EntityID), quote(m.Cursor), quote(m.SyncedAt), quote(m.Table), quote(m.Source), quote(m.EntityType), quote(m.EntityID))
	err := s.db.QueryRowContext(ctx, query, source, entityType, entityID).Scan(&rec.Source, &rec.EntityType, &rec.EntityID, &rec.Cursor, &syncedAt)
	if err == sql.ErrNoRows {
		return CursorRecord{}, false, nil
	}
	if err != nil {
		return CursorRecord{}, false, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, syncedAt)
	if err != nil {
		return CursorRecord{}, false, fmt.Errorf("parse cursor sync state synced_at: %w", err)
	}
	rec.SyncedAt = parsed
	return rec, true, nil
}

func (s *CursorStore) IsStale(ctx context.Context, source, entityType, entityID string, maxAge time.Duration) (bool, error) {
	rec, ok, err := s.Get(ctx, source, entityType, entityID)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if maxAge <= 0 {
		return false, nil
	}
	return s.now().UTC().Sub(rec.SyncedAt) > maxAge, nil
}

func defaultScopedMapping() ScopedMapping {
	return ScopedMapping{Table: "sync_scoped_state", Scope: "scope", Cursor: "cursor", UpdatedAt: "updated_at"}
}

func normalizeScopedMapping(mapping ScopedMapping) (ScopedMapping, error) {
	if mapping == (ScopedMapping{}) {
		mapping = defaultScopedMapping()
	}
	if err := validateIdentifiers(mapping.Table, mapping.Scope, mapping.Cursor, mapping.UpdatedAt); err != nil {
		return ScopedMapping{}, err
	}
	return mapping, nil
}

func defaultCursorMapping() CursorMapping {
	return CursorMapping{Table: "sync_cursor_state", Source: "source", EntityType: "entity_type", EntityID: "entity_id", Cursor: "cursor", SyncedAt: "synced_at"}
}

func normalizeCursorMapping(mapping CursorMapping) (CursorMapping, error) {
	if mapping == (CursorMapping{}) {
		mapping = defaultCursorMapping()
	}
	if err := validateIdentifiers(mapping.Table, mapping.Source, mapping.EntityType, mapping.EntityID, mapping.Cursor, mapping.SyncedAt); err != nil {
		return CursorMapping{}, err
	}
	return mapping, nil
}

func validateIdentifiers(values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\"\x00") {
			return fmt.Errorf("invalid sync state identifier %q", value)
		}
	}
	return nil
}

func quote(value string) string {
	return `"` + value + `"`
}
