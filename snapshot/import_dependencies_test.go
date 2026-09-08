package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func dependencyFixture(t *testing.T, action string, exportedChild bool) (*sql.DB, IncrementalImportOptions) {
	t.Helper()
	schema := `create table things(id integer primary key, value text);
create table children(id integer primary key, parent_id integer references THINGS(id) on delete ` + action + `);`
	src := exportTestDB(t, schema+`insert into things values(1,'first'); insert into children values(1,1);`)
	tables := []string{"things"}
	if exportedChild {
		tables = append(tables, "children")
	}
	root := t.TempDir()
	export := ExportOptions{DB: src, RootDir: root, Tables: tables}
	first, err := Export(context.Background(), export)
	if err != nil {
		t.Fatal(err)
	}
	dst := exportTestDB(t, schema)
	if _, err := Import(context.Background(), ImportOptions{DB: dst, RootDir: root}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, dst, `insert into children values(2,1)`)
	mustExec(t, src, `update things set value='second'`)
	current, err := Export(context.Background(), export)
	if err != nil {
		t.Fatal(err)
	}
	return dst, IncrementalImportOptions{DB: dst, RootDir: root, Previous: first, Current: current}
}

func assertDependencyRows(t *testing.T, db *sql.DB, wantChildren int, wantValue string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from children where parent_id=1`).Scan(&count); err != nil || count != wantChildren {
		t.Fatalf("dependent history changed: count=%d err=%v", count, err)
	}
	var value string
	if err := db.QueryRow(`select value from things where id=1`).Scan(&value); err != nil || value != wantValue {
		t.Fatalf("parent value=%q err=%v", value, err)
	}
}

func TestIncrementalRejectsDestructiveForeignKeysBeforeHooks(t *testing.T) {
	for _, action := range []string{"CASCADE", "SET NULL", "SET DEFAULT"} {
		for _, exportedChild := range []bool{false, true} {
			for _, merge := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/child=%t/merge=%t", action, exportedChild, merge), func(t *testing.T) {
					db, opts := dependencyFixture(t, action, exportedChild)
					if merge {
						opts.Plan = PlanMergeImport(opts.Previous, opts.Current)
						if opts.Plan.Tables[0].Mode != TableImportFiles {
							t.Fatal("fixture did not exercise partial-file INSERT OR REPLACE")
						}
					}
					hookCalled := false
					opts.BeforeImport = func(context.Context, *sql.Tx) error { hookCalled = true; return nil }
					_, _, err := ImportIncremental(context.Background(), opts)
					if err == nil || !strings.Contains(err.Error(), "ON DELETE "+action) || hookCalled {
						t.Fatalf("dependency not refused before hooks: err=%v hook=%v", err, hookCalled)
					}
					count := 1
					if exportedChild {
						count++
					}
					assertDependencyRows(t, db, count, "first")
				})
			}
		}
	}
}

func TestIncrementalDependencyAwareCallbacks(t *testing.T) {
	db, opts := dependencyFixture(t, "CASCADE", true)
	upsert := func(ctx context.Context, tx *sql.Tx, _ string, row map[string]any) error {
		_, err := tx.ExecContext(ctx, `insert into things(id,value) values(?,?)
on conflict(id) do update set value=excluded.value`, row["id"], row["value"])
		return err
	}
	noDelete := func(context.Context, *sql.Tx, string) error { return nil }
	opts.ImportRow = upsert
	if _, _, err := ImportIncremental(context.Background(), opts); err == nil {
		t.Fatal("custom importer did not protect the remaining generic delete")
	}
	opts.ImportRow = nil
	opts.DeleteTable = noDelete
	if _, _, err := ImportIncremental(context.Background(), opts); err == nil {
		t.Fatal("custom delete did not protect the remaining generic replacement")
	}
	assertDependencyRows(t, db, 2, "first")
	opts.ImportRow = upsert
	if _, _, err := ImportIncremental(context.Background(), opts); err != nil {
		t.Fatalf("dependency-aware callbacks refused: %v", err)
	}
	assertDependencyRows(t, db, 2, "second")
}

func TestIncrementalRestrictiveForeignKeysRollBack(t *testing.T) {
	for _, action := range []string{"RESTRICT", "NO ACTION"} {
		t.Run(action, func(t *testing.T) {
			db, opts := dependencyFixture(t, action, true)
			mustExec(t, db, `create table hooks(value text)`)
			called := false
			opts.BeforeImport = func(ctx context.Context, tx *sql.Tx) error {
				called = true
				_, err := tx.ExecContext(ctx, `insert into hooks values('before')`)
				return err
			}
			if _, _, err := ImportIncremental(context.Background(), opts); err == nil || !called {
				t.Fatalf("expected ordinary constraint failure, not cascade preflight: err=%v called=%v", err, called)
			}
			assertDependencyRows(t, db, 2, "first")
			var count int
			if err := db.QueryRow(`select count(*) from hooks`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("constraint failure did not roll back hook: %d %v", count, err)
			}
		})
	}
}

func TestIncrementalRejectsTriggersBeforeHooks(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		t.Run(fmt.Sprint(temporary), func(t *testing.T) {
			src := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'second');`)
			root := t.TempDir()
			current, err := Export(context.Background(), ExportOptions{DB: src, RootDir: root, Tables: []string{"things"}})
			if err != nil {
				t.Fatal(err)
			}
			dst := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'first');
create table children(id integer primary key, parent_id integer); insert into children values(1,1);`)
			dst.SetMaxOpenConns(1)
			prefix := ""
			if temporary {
				prefix = "temp "
			}
			mustExec(t, dst, `create `+prefix+`trigger remove_child after delete on THINGS begin delete from children; end`)
			hookCalled := false
			_, _, err = ImportIncremental(context.Background(), IncrementalImportOptions{
				DB: dst, RootDir: root, Current: current,
				Plan:         ImportPlan{Tables: []TableImportPlan{{Table: current.Tables[0], Mode: TableImportReplace}}},
				BeforeImport: func(context.Context, *sql.Tx) error { hookCalled = true; return nil },
			})
			if err == nil || !strings.Contains(err.Error(), "trigger") || hookCalled {
				t.Fatalf("trigger not refused before hooks: err=%v hook=%v", err, hookCalled)
			}
			assertDependencyRows(t, dst, 1, "first")
		})
	}
}

func TestIncrementalRefusesShadowedGenericTarget(t *testing.T) {
	db := exportTestDB(t, `create table things(id integer);`)
	db.SetMaxOpenConns(1)
	mustExec(t, db, `create temp table things(id integer); insert into things values(7)`)
	hookCalled := false
	_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
		DB: db, Current: Manifest{Version: 1, Tables: []TableManifest{{Name: "things"}}},
		Plan:         ImportPlan{Tables: []TableImportPlan{{Table: TableManifest{Name: "things"}, Mode: TableImportReplace}}},
		BeforeImport: func(context.Context, *sql.Tx) error { hookCalled = true; return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "unshadowed") || hookCalled {
		t.Fatalf("shadowed target not refused: err=%v hook=%v", err, hookCalled)
	}
	var id int
	if err := db.QueryRow(`select id from things`).Scan(&id); err != nil || id != 7 {
		t.Fatalf("shadowed table mutated: %d %v", id, err)
	}
}

func TestIncrementalEmptyReplacementCustomDelete(t *testing.T) {
	for _, file := range []string{"", " \t "} {
		t.Run(fmt.Sprintf("file=%q", file), func(t *testing.T) {
			db := exportTestDB(t, `create table things(id integer primary key, value text);
create table children(id integer primary key, parent_id integer references things(id) on delete cascade);
insert into things values(1,'retained'); insert into children values(1,1);`)
			table := TableManifest{Name: "things", File: file}
			hookCalled, deleteCalled := false, false
			opts := IncrementalImportOptions{
				DB: db, Current: Manifest{Version: 1, Tables: []TableManifest{table}},
				Plan:         ImportPlan{Tables: []TableImportPlan{{Table: table, Mode: TableImportReplace}}},
				BeforeImport: func(context.Context, *sql.Tx) error { hookCalled = true; return nil },
			}
			if _, _, err := ImportIncremental(context.Background(), opts); err == nil || hookCalled {
				t.Fatalf("empty replacement still needs a safe delete: err=%v hook=%v", err, hookCalled)
			}
			opts.DeleteTable = func(context.Context, *sql.Tx, string) error { deleteCalled = true; return nil }
			if _, _, err := ImportIncremental(context.Background(), opts); err != nil || !deleteCalled || !hookCalled {
				t.Fatalf("unused ImportRow callback required: err=%v delete=%v hook=%v", err, deleteCalled, hookCalled)
			}
			assertDependencyRows(t, db, 1, "retained")
		})
	}
}

func TestIncrementalLegacyFileStillNeedsSafeImporter(t *testing.T) {
	db, opts := dependencyFixture(t, "CASCADE", true)
	table := opts.Current.Tables[0]
	table.File = table.Files[0]
	table.Files = nil
	opts.Plan = ImportPlan{Tables: []TableImportPlan{{Table: table, Mode: TableImportReplace}}}
	opts.DeleteTable = func(context.Context, *sql.Tx, string) error { return nil }
	if _, _, err := ImportIncremental(context.Background(), opts); err == nil {
		t.Fatal("legacy singular file can still execute a generic replacement")
	}
	assertDependencyRows(t, db, 2, "first")
}
