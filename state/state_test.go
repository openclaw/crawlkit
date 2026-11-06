package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSetGetAndStale(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := NewWithClock(db, func() time.Time { return now })
	if err := store.Set(ctx, "share", "repo", "last_import", "2026-05-01T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := store.Get(ctx, "share", "repo", "last_import")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || rec.Value == "" {
		t.Fatalf("record not found: %+v", rec)
	}
	stale, err := store.IsStale(ctx, "share", "repo", "last_import", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("fresh record reported stale")
	}
	store.now = func() time.Time { return now.Add(2 * time.Hour) }
	stale, err = store.IsStale(ctx, "share", "repo", "last_import", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("old record reported fresh")
	}
}

func TestScopedStoreSetGetAndStale(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "scoped.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureScopedSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := NewScopedWithClock(db, func() time.Time { return now })
	if err := store.Set(ctx, "share:last_import_at", "2026-05-01T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := store.Get(ctx, "share:last_import_at")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || rec.Cursor == "" {
		t.Fatalf("record not found: %+v", rec)
	}
	stale, err := store.IsStale(ctx, "share:last_import_at", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("fresh scoped record reported stale")
	}
	store.now = func() time.Time { return now.Add(2 * time.Hour) }
	stale, err = store.IsStale(ctx, "share:last_import_at", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("old scoped record reported fresh")
	}
}

func TestCursorStoreSetGetAndStale(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "cursor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureCursorSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := NewCursorWithClock(db, func() time.Time { return now })
	if err := store.Set(ctx, "share", "manifest", "generated_at", "2026-05-01T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := store.Get(ctx, "share", "manifest", "generated_at")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || rec.Cursor == "" {
		t.Fatalf("record not found: %+v", rec)
	}
	stale, err := store.IsStale(ctx, "share", "manifest", "generated_at", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("fresh cursor record reported stale")
	}
	store.now = func() time.Time { return now.Add(2 * time.Hour) }
	stale, err = store.IsStale(ctx, "share", "manifest", "generated_at", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("old cursor record reported fresh")
	}
}

func TestStateSchemasCoexistInOneDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureScopedSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCursorSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	source, entityType, entityID := ManifestKey("share")
	if source != "share" || entityType != "manifest" || entityID != "generated_at" {
		t.Fatalf("manifest key = %q, %q, %q", source, entityType, entityID)
	}
	store := New(db)
	scoped := NewScoped(db)
	cursor := NewCursor(db)
	if err := store.Set(ctx, source, entityType, entityID, "value"); err != nil {
		t.Fatal(err)
	}
	if err := scoped.Set(ctx, "share:last_import_at", "scoped-cursor"); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Set(ctx, "share", "manifest", "generated_at", "cursor"); err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := store.Get(ctx, source, entityType, entityID); err != nil || !ok || rec.SourceName != "share" || rec.EntityType != "manifest" || rec.EntityID != "generated_at" || rec.Value != "value" {
		t.Fatalf("manifest record = %+v ok=%v err=%v", rec, ok, err)
	}
	if rec, ok, err := scoped.Get(ctx, "share:last_import_at"); err != nil || !ok || rec.Scope != "share:last_import_at" || rec.Cursor != "scoped-cursor" {
		t.Fatalf("scoped record = %+v ok=%v err=%v", rec, ok, err)
	}
	if rec, ok, err := cursor.Get(ctx, "share", "manifest", "generated_at"); err != nil || !ok || rec.Source != "share" || rec.EntityType != "manifest" || rec.EntityID != "generated_at" || rec.Cursor != "cursor" {
		t.Fatalf("cursor record = %+v ok=%v err=%v", rec, ok, err)
	}
}

func TestMappedStoresUseExistingSyncStateTables(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mapped.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
create table discrawl_state(scope text primary key, cursor text not null, updated_at text not null);
create table notcrawl_state(source text not null, kind text not null, entity text not null, cursor text not null, synced_at text not null, primary key(source, kind, entity));
`); err != nil {
		t.Fatal(err)
	}
	scoped, err := NewScopedMapped(db, ScopedMapping{Table: "discrawl_state", Scope: "scope", Cursor: "cursor", UpdatedAt: "updated_at"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.Set(ctx, "sync:last", "123"); err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := scoped.Get(ctx, "sync:last"); err != nil || !ok || rec.Cursor != "123" {
		t.Fatalf("mapped scoped record = %+v ok=%v err=%v", rec, ok, err)
	}

	cursor, err := NewCursorMapped(db, CursorMapping{Table: "notcrawl_state", Source: "source", EntityType: "kind", EntityID: "entity", Cursor: "cursor", SyncedAt: "synced_at"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cursor.Set(ctx, "api", "page", "one", "done"); err != nil {
		t.Fatal(err)
	}
	if rec, ok, err := cursor.Get(ctx, "api", "page", "one"); err != nil || !ok || rec.Cursor != "done" {
		t.Fatalf("mapped cursor record = %+v ok=%v err=%v", rec, ok, err)
	}
	if _, err := NewScopedMapped(db, ScopedMapping{Table: `bad"table`, Scope: "scope", Cursor: "cursor", UpdatedAt: "updated_at"}); err == nil {
		t.Fatal("unsafe mapped identifier should fail")
	}
}
