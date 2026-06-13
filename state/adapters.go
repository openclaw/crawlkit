package state

import (
	"context"
	"database/sql"
	"fmt"
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
	db  execQuerier
	now func() time.Time
}

type ScopedRecord struct {
	Scope     string    `json:"scope"`
	Cursor    string    `json:"cursor"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CursorStore struct {
	db  execQuerier
	now func() time.Time
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
	return &ScopedStore{db: db, now: now}
}

func (s *ScopedStore) Set(ctx context.Context, scope, cursor string) error {
	updatedAt := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
insert into sync_scoped_state(scope, cursor, updated_at)
values (?, ?, ?)
on conflict(scope) do update set
  cursor = excluded.cursor,
  updated_at = excluded.updated_at
`, scope, cursor, updatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set scoped sync state: %w", err)
	}
	return nil
}

func (s *ScopedStore) Get(ctx context.Context, scope string) (ScopedRecord, bool, error) {
	var rec ScopedRecord
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
select scope, cursor, updated_at
from sync_scoped_state
where scope = ?
`, scope).Scan(&rec.Scope, &rec.Cursor, &updatedAt)
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
	return &CursorStore{db: db, now: now}
}

func (s *CursorStore) Set(ctx context.Context, source, entityType, entityID, cursor string) error {
	syncedAt := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
insert into sync_cursor_state(source, entity_type, entity_id, cursor, synced_at)
values (?, ?, ?, ?, ?)
on conflict(source, entity_type, entity_id) do update set
  cursor = excluded.cursor,
  synced_at = excluded.synced_at
`, source, entityType, entityID, cursor, syncedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set cursor sync state: %w", err)
	}
	return nil
}

func (s *CursorStore) Get(ctx context.Context, source, entityType, entityID string) (CursorRecord, bool, error) {
	var rec CursorRecord
	var syncedAt string
	err := s.db.QueryRowContext(ctx, `
select source, entity_type, entity_id, cursor, synced_at
from sync_cursor_state
where source = ? and entity_type = ? and entity_id = ?
`, source, entityType, entityID).Scan(&rec.Source, &rec.EntityType, &rec.EntityID, &rec.Cursor, &syncedAt)
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
