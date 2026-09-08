package snapshot

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/internal/filelock"
	"github.com/openclaw/crawlkit/store"
)

func exportTestDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	s, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(t.TempDir(), "test.db"), Schema: schema, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.DB()
}

func packBytes(t *testing.T, root string, manifest Manifest) map[string][]byte {
	t.Helper()
	paths := []string{ManifestName}
	for _, table := range manifest.Tables {
		paths = append(paths, table.Files...)
	}
	out := map[string][]byte{}
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("pack file %s: %v", rel, err)
		}
		out[rel] = data
	}
	return out
}

func TestExportFailurePreservesPreviousPack(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'old');`)
	root := t.TempDir()
	first, err := Export(context.Background(), ExportOptions{DB: db, RootDir: root, Tables: []string{"things"}})
	if err != nil {
		t.Fatal(err)
	}
	before := packBytes(t, root, first)
	mustExec(t, db, `update things set value='new'`)
	if _, err := Export(context.Background(), ExportOptions{DB: db, RootDir: root, Tables: []string{"things", "missing"}}); err == nil {
		t.Fatal("missing table accepted")
	}
	if !reflect.DeepEqual(before, packBytes(t, root, first)) {
		t.Fatal("failed export changed prior pack")
	}
}

func TestExportOneReadSnapshotAcrossTables(t *testing.T) {
	db := exportTestDB(t, `create table parents(id integer, version integer); create table children(id integer, version integer);
insert into parents values(1,1); insert into children values(1,1);`)
	root := t.TempDir()
	wrote := false
	_, err := Export(context.Background(), ExportOptions{DB: db, RootDir: root, Tables: []string{"parents", "children"},
		Filter: func(table string, row map[string]any) (bool, error) {
			if table == "parents" && !wrote {
				wrote = true
				_, err := db.Exec(`begin; update parents set version=2; update children set version=2; commit;`)
				return true, err
			}
			return true, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	dst := exportTestDB(t, `create table parents(id integer, version integer); create table children(id integer, version integer);`)
	if _, err := Import(context.Background(), ImportOptions{DB: dst, RootDir: root}); err != nil {
		t.Fatal(err)
	}
	var parent, child int
	if err := dst.QueryRow(`select parents.version, children.version from parents,children`).Scan(&parent, &child); err != nil {
		t.Fatal(err)
	}
	if parent != 1 || child != 1 {
		t.Fatalf("incoherent exported versions: parent=%d child=%d", parent, child)
	}
}

func TestExportCallerTransactionAndFilterOrder(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'old'),(2,'excluded');`)
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update things set value='uncommitted' where id=1`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = Export(context.Background(), ExportOptions{
		ReadTx: tx, RootDir: t.TempDir(), Tables: []string{"things"},
		Filter: func(_ string, row map[string]any) (bool, error) {
			if row["id"] == int64(2) {
				return false, nil
			}
			row["value"] = "legacy filter ran"
			return true, nil
		},
		FilterTx: func(ctx context.Context, current *sql.Tx, _ string, row map[string]any) (bool, error) {
			calls++
			if current != tx || row["value"] != "legacy filter ran" {
				t.Fatal("transaction callback ownership or ordering changed")
			}
			var value string
			err := current.QueryRowContext(ctx, `select value from things where id=1`).Scan(&value)
			if err == nil && value != "uncommitted" {
				t.Fatalf("callback read another database view: %q", value)
			}
			return true, err
		},
	})
	if err != nil || calls != 1 {
		t.Fatalf("export/callback: calls=%d error=%v", calls, err)
	}
	if _, err := tx.Exec(`update things set value='still owned' where id=1`); err != nil {
		t.Fatalf("export ended caller transaction: %v", err)
	}
	if _, err := Export(context.Background(), ExportOptions{ReadTx: tx, RootDir: t.TempDir(), Tables: []string{"missing"}}); err == nil {
		t.Fatal("expected failed export")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("failed export ended caller transaction: %v", err)
	}
	var value string
	if err := db.QueryRow(`select value from things where id=1`).Scan(&value); err != nil || value != "old" {
		t.Fatalf("caller rollback lost ownership: %q, %v", value, err)
	}
}

func TestExportGenerationReuseAndExactCleanup(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'first'),(2,'second');`)
	opts := ExportOptions{DB: db, RootDir: t.TempDir(), Tables: []string{"things"}, MaxShardBytes: 1}
	first, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range first.Tables[0].Files {
		if logical, ok := logicalShardPath("things", rel); !ok || !strings.HasPrefix(rel, "tables/.generations/") || logical == rel {
			t.Fatalf("invalid generated path: %q", rel)
		}
	}
	unowned := filepath.Join(opts.RootDir, filepath.Dir(first.Tables[0].Files[0]), "unlisted.txt")
	if err := os.WriteFile(unowned, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	same, err := Export(context.Background(), opts)
	if err != nil || PlanIncrementalImport(first, same).Changed() || !reflect.DeepEqual(first.Tables[0].Files, same.Tables[0].Files) {
		t.Fatalf("unchanged physical reuse/plan: %+v, %v", same, err)
	}
	mustExec(t, db, `insert into things values(3,'third')`)
	appended, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanIncrementalImport(same, appended)
	if plan.Tables[0].Mode != TableImportFiles || len(plan.Tables[0].Files) != 1 {
		t.Fatalf("append plan: %+v", plan)
	}
	mustExec(t, db, `update things set value='changed third' where id=3`)
	changed, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if PlanIncrementalImport(appended, changed).Tables[0].Mode != TableImportReplace {
		t.Fatal("changed-tail exact replacement contract changed")
	}
	if plan := PlanMergeImport(appended, changed); plan.Tables[0].Mode != TableImportFiles || len(plan.Tables[0].Files) != 1 {
		t.Fatalf("changed-tail merge plan: %+v", plan)
	}
	if data, err := os.ReadFile(unowned); err != nil || string(data) != "keep" {
		t.Fatalf("unlisted generation file removed: %q, %v", data, err)
	}
	oldTail := appended.Tables[0].Files[2]
	if _, err := os.Stat(filepath.Join(opts.RootDir, oldTail)); !os.IsNotExist(err) {
		t.Fatalf("obsolete owned shard retained: %v", err)
	}
}

func TestExportChecksBytesBeforeLegacyReuse(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer); insert into things values(1);`)
	opts := ExportOptions{DB: db, RootDir: t.TempDir(), Tables: []string{"things"}}
	first, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	legacy := "tables/things/000000.jsonl.gz"
	if err := os.MkdirAll(filepath.Join(opts.RootDir, "tables/things"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(opts.RootDir, first.Tables[0].Files[0]), filepath.Join(opts.RootDir, legacy)); err != nil {
		t.Fatal(err)
	}
	first.Tables[0].Files[0], first.Tables[0].FileManifests[0].Path = legacy, legacy
	if err := WriteManifest(opts.RootDir, first); err != nil {
		t.Fatal(err)
	}
	reused, err := Export(context.Background(), opts)
	if err != nil || reused.Tables[0].Files[0] != legacy {
		t.Fatalf("valid legacy shard not reused: %+v, %v", reused, err)
	}
	if err := os.WriteFile(filepath.Join(opts.RootDir, legacy), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := Export(context.Background(), opts)
	if err != nil || repaired.Tables[0].Files[0] == legacy {
		t.Fatalf("claimed hash trusted without actual bytes: %+v, %v", repaired, err)
	}
}

func TestExportCleanupFailureReturnsCommittedManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix directory permissions")
	}
	db := exportTestDB(t, `create table things(id integer); insert into things values(1);`)
	opts := ExportOptions{DB: db, RootDir: t.TempDir(), Tables: []string{"things"}}
	first, err := Export(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(filepath.Join(opts.RootDir, first.Tables[0].Files[0]))
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o755)
	mustExec(t, db, `insert into things values(2)`)
	result, err := Export(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "committed; cleanup failed") {
		t.Fatalf("cleanup outcome = %v", err)
	}
	actual, readErr := ReadManifest(opts.RootDir)
	if readErr != nil || result.Version != 1 || !reflect.DeepEqual(actual, result) {
		t.Fatalf("missing committed manifest: %+v, %v", result, readErr)
	}
}

func TestExportHeldLockAndRootConfinement(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer); insert into things values(1);`)
	root := t.TempDir()
	lock, err := filelock.Acquire(filepath.Join(root, ".crawlkit-snapshot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	opts := ExportOptions{DB: db, RootDir: root, Tables: []string{"things"}}
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("held writer lock ignored")
	}
	_ = lock.Close()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "retained")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tables")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Export(context.Background(), opts); err == nil {
		t.Fatal("output followed an outside-root table directory")
	}
	if data, err := os.ReadFile(sentinel); err != nil || !bytes.Equal(data, []byte("outside")) {
		t.Fatalf("outside directory changed: %q, %v", data, err)
	}
}

func TestPlanStrictGenerationLogicalMatching(t *testing.T) {
	file := FileManifest{Path: "tables/things/000000.jsonl.gz", Rows: 1, Size: 50, SHA256: "same"}
	table := TableManifest{Name: "things", Columns: []string{"id"}, FileManifests: []FileManifest{file}}
	generated := "tables/.generations/" + strings.Repeat("a", 32) + "/things/000000.jsonl.gz"
	for _, rel := range []string{generated, strings.Replace(generated, "aaaa", "AAAA", 1), strings.Replace(generated, "/things/", "/other/", 1), strings.Replace(generated, "000000", "0", 1)} {
		current := table
		next := file
		next.Path = rel
		current.FileManifests = []FileManifest{next}
		for _, merge := range []bool{false, true} {
			plan := planTableIncrement(table, current, merge)
			if (plan.Mode == TableImportSkip) != (rel == generated) {
				t.Fatalf("logical matching %q merge=%v: %+v", rel, merge, plan)
			}
		}
	}
	if _, managed := logicalShardPath("..", "tables/../000000.jsonl.gz"); managed {
		t.Fatal("unsafe manifest table made a path appear exporter-owned")
	}
}
