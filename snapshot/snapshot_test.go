package snapshot

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/store"
)

func TestExportImportTablesWithFilter(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path: filepath.Join(t.TempDir(), "src.db"),
		Schema: `
create table messages(id text primary key, guild_id text not null, body text not null);
create table sync_state(source_name text, entity_type text, entity_id text, value text, updated_at text, primary key(source_name, entity_type, entity_id));
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mustExec(t, src.DB(), `insert into messages(id, guild_id, body) values('1', 'guild', 'hello')`)
	mustExec(t, src.DB(), `insert into messages(id, guild_id, body) values('2', '@me', 'private')`)
	mustExec(t, src.DB(), `insert into sync_state(source_name, entity_type, entity_id, value, updated_at) values('api', 'cursor', 'x', '1', 'now')`)

	root := t.TempDir()
	manifest, err := Export(ctx, ExportOptions{
		DB:      src.DB(),
		RootDir: root,
		Tables:  []string{"messages", "sync_state"},
		Now:     func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		Filter: func(table string, row map[string]any) (bool, error) {
			return !(table == "messages" && row["guild_id"] == "@me"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables) != 2 || manifest.Tables[0].Rows != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}

	dst, err := store.Open(ctx, store.Options{
		Path: filepath.Join(t.TempDir(), "dst.db"),
		Schema: `
create table messages(id text primary key, guild_id text not null, body text not null);
create table sync_state(source_name text, entity_type text, entity_id text, value text, updated_at text, primary key(source_name, entity_type, entity_id));
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, ImportOptions{DB: dst.DB(), RootDir: root}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dst.DB().QueryRowContext(ctx, `select count(*) from messages`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("message count = %d", count)
	}
}

func TestExportRotatesShards(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "src.db"),
		Schema: `create table things(id integer primary key, value text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	for i := 0; i < 25; i++ {
		mustExec(t, src.DB(), `insert into things(value) values('some repeated text to force shard rotation')`)
	}
	manifest, err := Export(ctx, ExportOptions{
		DB:            src.DB(),
		RootDir:       t.TempDir(),
		Tables:        []string{"things"},
		MaxShardBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables[0].Files) < 2 {
		t.Fatalf("expected multiple shards, got %+v", manifest.Tables[0].Files)
	}
	if len(manifest.Tables[0].FileManifests) != len(manifest.Tables[0].Files) {
		t.Fatalf("file manifests = %+v, files = %+v", manifest.Tables[0].FileManifests, manifest.Tables[0].Files)
	}
	for _, file := range manifest.Tables[0].FileManifests {
		if file.Path == "" || file.Rows == 0 || file.Size == 0 || len(file.SHA256) != 64 {
			t.Fatalf("bad file manifest = %+v", file)
		}
	}
}

func TestExportRejectsUnsafeTablePath(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "src.db"),
		Schema: `create table "../escape"(id text primary key);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	root := t.TempDir()
	_, err = Export(ctx, ExportOptions{
		DB:      src.DB(),
		RootDir: root,
		Tables:  []string{"../escape"},
	})
	if err == nil {
		t.Fatal("expected unsafe table path error")
	}
	if _, statErr := os.Stat(filepath.Join(root, "..", "escape")); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected escaped path stat err = %v", statErr)
	}
}

func TestSyncSidecarTreeCopiesFingerprintsAndPrunes(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "page.md"), []byte("page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "other.txt"), []byte("skip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "pages", "stale.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(root, "pages", "README.txt")
	if err := os.WriteFile(preserved, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecars, err := SyncSidecarTree(ctx, SidecarTreeOptions{
		SourceDir: source,
		RootDir:   root,
		TargetDir: "pages",
		Kind:      "markdown",
		Include:   func(rel string) bool { return strings.HasSuffix(rel, ".md") },
		Prune:     func(rel string) bool { return strings.HasSuffix(rel, ".md") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sidecars) != 1 || sidecars[0].Path != "pages/page.md" || sidecars[0].Size != 5 || len(sidecars[0].SHA256) != 64 {
		t.Fatalf("sidecars = %+v", sidecars)
	}
	if _, err := os.Stat(filepath.Join(root, "pages", "page.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale sidecar should be pruned: %v", err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("excluded sidecar should be preserved: %v", err)
	}
	if _, err := SyncSidecarTree(ctx, SidecarTreeOptions{SourceDir: source, RootDir: root, TargetDir: "../escape"}); err == nil {
		t.Fatal("escaping sidecar target should fail")
	}
	if _, err := SyncSidecarTree(ctx, SidecarTreeOptions{SourceDir: source, RootDir: source, TargetDir: "pages"}); err == nil {
		t.Fatal("overlapping sidecar trees should fail")
	}
}

func TestPlanIncrementalImportDetectsTailFiles(t *testing.T) {
	previous := Manifest{
		Version: 1,
		Tables: []TableManifest{{
			Name:    "things",
			Columns: []string{"id", "body"},
			Rows:    1,
			Files:   []string{"tables/things/000000.jsonl.gz"},
			FileManifests: []FileManifest{{
				Path:   "tables/things/000000.jsonl.gz",
				Rows:   1,
				Size:   100,
				SHA256: "same",
			}},
		}},
	}
	current := Manifest{
		Version: 1,
		Tables: []TableManifest{{
			Name:    "things",
			Columns: []string{"id", "body"},
			Rows:    2,
			Files:   []string{"tables/things/000000.jsonl.gz", "tables/things/000001.jsonl.gz"},
			FileManifests: []FileManifest{{
				Path:   "tables/things/000000.jsonl.gz",
				Rows:   1,
				Size:   100,
				SHA256: "same",
			}, {
				Path:   "tables/things/000001.jsonl.gz",
				Rows:   1,
				Size:   20,
				SHA256: "new",
			}},
		}},
	}
	plan := PlanIncrementalImport(previous, current)
	if plan.Full || len(plan.Tables) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	table := plan.Tables[0]
	if table.Mode != TableImportFiles || len(table.Files) != 1 || table.Files[0].SHA256 != "new" {
		t.Fatalf("table plan = %+v", table)
	}
}

func TestPlanIncrementalImportReplacesUnsafeTailChanges(t *testing.T) {
	previous := Manifest{
		Version: 1,
		Tables: []TableManifest{{
			Name:    "things",
			Columns: []string{"id", "body"},
			Rows:    2,
			Files:   []string{"tables/things/000000.jsonl.gz"},
			FileManifests: []FileManifest{{
				Path:   "tables/things/000000.jsonl.gz",
				Rows:   2,
				Size:   100,
				SHA256: "old",
			}},
		}},
	}
	current := Manifest{
		Version: 1,
		Tables: []TableManifest{{
			Name:    "things",
			Columns: []string{"id", "body"},
			Rows:    2,
			Files:   []string{"tables/things/000000.jsonl.gz"},
			FileManifests: []FileManifest{{
				Path:   "tables/things/000000.jsonl.gz",
				Rows:   2,
				Size:   100,
				SHA256: "new",
			}},
		}},
	}
	plan := PlanIncrementalImport(previous, current)
	if plan.Full || len(plan.Tables) != 1 || plan.Tables[0].Mode != TableImportReplace {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestImportIncrementalImportsOnlyPlannedFiles(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "src.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mustExec(t, src.DB(), `insert into things(id, body) values('one', 'same')`)
	root := t.TempDir()
	previous, err := Export(ctx, ExportOptions{
		DB:            src.DB(),
		RootDir:       root,
		Tables:        []string{"things"},
		MaxShardBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, ImportOptions{DB: dst.DB(), RootDir: root}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, dst.DB(), `insert into things(id, body) values('local', 'keep')`)

	mustExec(t, src.DB(), `insert into things(id, body) values('two', 'added')`)
	current, err := Export(ctx, ExportOptions{
		DB:            src.DB(),
		RootDir:       root,
		Tables:        []string{"things"},
		MaxShardBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := ImportIncremental(ctx, IncrementalImportOptions{
		DB:       dst.DB(),
		RootDir:  root,
		Previous: previous,
		Current:  current,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tables) != 1 || plan.Tables[0].Mode != TableImportFiles {
		t.Fatalf("plan = %+v", plan)
	}
	var got string
	if err := dst.DB().QueryRowContext(ctx, `select group_concat(id || ':' || body, ',') from (select id, body from things order by id)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "local:keep,one:same,two:added" {
		t.Fatalf("things = %q", got)
	}
}

func TestImportIncrementalReplacesChangedTailShard(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "src.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mustExec(t, src.DB(), `insert into things(id, body) values('one', 'same')`)
	mustExec(t, src.DB(), `insert into things(id, body) values('two', 'old')`)
	root := t.TempDir()
	previous, err := Export(ctx, ExportOptions{DB: src.DB(), RootDir: root, Tables: []string{"things"}})
	if err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, ImportOptions{DB: dst.DB(), RootDir: root}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, src.DB(), `delete from things where id = 'two'`)
	mustExec(t, src.DB(), `insert into things(id, body) values('three', 'added')`)
	current, err := Export(ctx, ExportOptions{DB: src.DB(), RootDir: root, Tables: []string{"things"}})
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := ImportIncremental(ctx, IncrementalImportOptions{
		DB:       dst.DB(),
		RootDir:  root,
		Previous: previous,
		Current:  current,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Tables) != 1 || plan.Tables[0].Mode != TableImportReplace {
		t.Fatalf("plan = %+v", plan)
	}
	var got string
	if err := dst.DB().QueryRowContext(ctx, `select group_concat(id || ':' || body, ',') from (select id, body from things order by id)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "one:same,three:added" {
		t.Fatalf("things = %q", got)
	}
}

func TestImportHooks(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "src.db"),
		Schema: `create table things(id text primary key, keep integer not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mustExec(t, src.DB(), `insert into things(id, keep) values('new', 1)`)
	root := t.TempDir()
	if _, err := Export(ctx, ExportOptions{DB: src.DB(), RootDir: root, Tables: []string{"things"}}); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table things(id text primary key, keep integer not null); create table audit(event text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	mustExec(t, dst.DB(), `insert into things(id, keep) values('local', 0)`)
	if _, err := Import(ctx, ImportOptions{
		DB:      dst.DB(),
		RootDir: root,
		BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `insert into audit(event) values('before')`)
			return err
		},
		DeleteTable: func(ctx context.Context, tx *sql.Tx, table string) error {
			_, err := tx.ExecContext(ctx, `delete from `+store.QuoteIdent(table)+` where keep != 0`)
			return err
		},
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dst.DB().QueryRowContext(ctx, `select count(*) from things where id = 'local'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("custom delete hook removed local row")
	}
}

func TestImportReportsTableAndFileProgress(t *testing.T) {
	ctx := context.Background()
	src, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "src.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	mustExec(t, src.DB(), `insert into things(id, body) values('one', 'keep')`)
	mustExec(t, src.DB(), `insert into things(id, body) values('two', 'skip')`)
	root := t.TempDir()
	if _, err := Export(ctx, ExportOptions{DB: src.DB(), RootDir: root, Tables: []string{"things"}}); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	var progress []ImportProgress
	if _, err := Import(ctx, ImportOptions{
		DB:      dst.DB(),
		RootDir: root,
		Filter: func(table string, row map[string]any) (bool, error) {
			return row["id"] != "two", nil
		},
		Progress: func(event ImportProgress) {
			progress = append(progress, event)
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"table_start", "file_start", "file_done", "table_done"} {
		if !containsImportPhase(progress, phase) {
			t.Fatalf("progress missing %q: %+v", phase, progress)
		}
	}
	if got := progress[len(progress)-1]; got.Phase != "table_done" || got.Table != "things" || got.Rows != 1 || got.TotalRows != 2 {
		t.Fatalf("table_done progress = %+v", got)
	}
}

func TestImportLegacySingularFileManifest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("tables", "things", "legacy.jsonl.gz"))
	writeGzipJSONL(t, filepath.Join(root, filepath.FromSlash(rel)), map[string]any{"id": "new", "body": "from legacy"})
	if err := WriteManifest(root, Manifest{
		Version:     1,
		GeneratedAt: time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
		Tables: []TableManifest{{
			Name:    "things",
			File:    rel,
			Columns: []string{"id", "body"},
			Rows:    1,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table things(id text primary key, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	mustExec(t, dst.DB(), `insert into things(id, body) values('old', 'before')`)
	if _, err := Import(ctx, ImportOptions{DB: dst.DB(), RootDir: root}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := dst.DB().QueryRowContext(ctx, `select group_concat(id || ':' || body, ',') from things`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "new:from legacy" {
		t.Fatalf("things = %q", got)
	}
}

func TestImportFilterSkipsRows(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("tables", "messages", "000000.jsonl.gz"))
	writeGzipJSONL(t,
		filepath.Join(root, filepath.FromSlash(rel)),
		map[string]any{"id": "public", "guild_id": "g1", "body": "keep"},
		map[string]any{"id": "dm", "guild_id": "@me", "body": "skip"},
	)
	if err := WriteManifest(root, Manifest{
		Version:     1,
		GeneratedAt: time.Date(2026, 5, 2, 9, 5, 0, 0, time.UTC),
		Tables: []TableManifest{{
			Name:    "messages",
			Files:   []string{rel},
			Columns: []string{"id", "guild_id", "body"},
			Rows:    2,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	dst, err := store.Open(ctx, store.Options{
		Path:   filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table messages(id text primary key, guild_id text not null, body text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := Import(ctx, ImportOptions{
		DB:      dst.DB(),
		RootDir: root,
		Filter: func(table string, row map[string]any) (bool, error) {
			return !(table == "messages" && row["guild_id"] == "@me"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dst.DB().QueryRowContext(ctx, `select count(*) from messages where guild_id = '@me'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("private rows imported = %d", count)
	}
}

func containsImportPhase(progress []ImportProgress, phase string) bool {
	for _, event := range progress {
		if event.Phase == phase {
			return true
		}
	}
	return false
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func writeGzipJSONL(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	enc := json.NewEncoder(gz)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
