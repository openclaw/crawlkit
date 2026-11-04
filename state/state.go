package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const Schema = `
create table if not exists sync_state (
  source_name text not null,
  entity_type text not null,
  entity_id text not null,
  value text not null,
  updated_at text not null,
  primary key (source_name, entity_type, entity_id)
);
create index if not exists idx_sync_state_updated_at on sync_state(updated_at desc);
`

type Store struct {
	db  execQuerier
	now func() time.Time
}

type Record struct {
	SourceName string    `json:"source_name"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	Value      string    `json:"value"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type execQuerier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func New(db execQuerier) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func NewWithClock(db execQuerier, now func() time.Time) *Store {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{db: db, now: now}
}

func EnsureSchema(ctx context.Context, db execQuerier) error {
	if _, err := db.ExecContext(ctx, Schema); err != nil {
		return fmt.Errorf("ensure sync_state schema: %w", err)
	}
	return nil
}

func (s *Store) Set(ctx context.Context, sourceName, entityType, entityID, value string) error {
	updatedAt := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
insert into sync_state(source_name, entity_type, entity_id, value, updated_at)
values (?, ?, ?, ?, ?)
on conflict(source_name, entity_type, entity_id) do update set
  value = excluded.value,
  updated_at = excluded.updated_at
`, sourceName, entityType, entityID, value, updatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("set sync state: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, sourceName, entityType, entityID string) (Record, bool, error) {
	var rec Record
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
select source_name, entity_type, entity_id, value, updated_at
from sync_state
where source_name = ? and entity_type = ? and entity_id = ?
`, sourceName, entityType, entityID).Scan(&rec.SourceName, &rec.EntityType, &rec.EntityID, &rec.Value, &updatedAt)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Record{}, false, fmt.Errorf("parse sync state updated_at: %w", err)
	}
	rec.UpdatedAt = parsed
	return rec, true, nil
}

func (s *Store) IsStale(ctx context.Context, sourceName, entityType, entityID string, maxAge time.Duration) (bool, error) {
	rec, ok, err := s.Get(ctx, sourceName, entityType, entityID)
	if err != nil {
		return false, err
	}
	return recordIsStale(ok, rec.UpdatedAt, maxAge, s.now), nil
}

func ManifestKey(sourceName string) (string, string, string) {
	return sourceName, "manifest", "generated_at"
}

func recordIsStale(found bool, updatedAt time.Time, maxAge time.Duration, now func() time.Time) bool {
	if !found {
		return true
	}
	if maxAge <= 0 {
		return false
	}
	return now().UTC().Sub(updatedAt) > maxAge
}
