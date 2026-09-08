package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func compositionInputs(t *testing.T, current Manifest, plan ImportPlan) []byte {
	t.Helper()
	data, err := json.Marshal([]any{current, plan})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImportCompositionPreflightUsesEffectiveFiles(t *testing.T) {
	for _, dependency := range []string{"CASCADE", "SET NULL", "trigger"} {
		for _, form := range []string{"forged empty plan", "metadata only"} {
			t.Run(dependency+"/"+form, func(t *testing.T) {
				src := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'new');`)
				root := t.TempDir()
				current, err := Export(context.Background(), ExportOptions{DB: src, RootDir: root, Tables: []string{"things"}})
				if err != nil {
					t.Fatal(err)
				}
				parentColumn := "parent_id integer"
				if dependency != "trigger" {
					parentColumn += " references things(id) on delete " + dependency
				}
				dst := exportTestDB(t, `create table things(id integer primary key, value text);
create table children(id integer primary key, `+parentColumn+`);
create table audit(event text);
insert into things values(1,'old'); insert into children values(1,1);`)
				if dependency == "trigger" {
					mustExec(t, dst, `create trigger remove_children after insert on things begin delete from children; end`)
				}
				planned := TableManifest{Name: "things"}
				if form == "metadata only" {
					current.Tables[0].Files = nil
					planned = current.Tables[0]
				}
				plan := ImportPlan{Tables: []TableImportPlan{{Table: planned, Mode: TableImportReplace}}}
				before := compositionInputs(t, current, plan)
				hookCalled, deleteCalled := false, false
				_, returned, err := ImportIncremental(context.Background(), IncrementalImportOptions{
					DB: dst, RootDir: root, Current: current, Plan: plan,
					DeleteTable: func(context.Context, *sql.Tx, string) error { deleteCalled = true; return nil },
					BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
						hookCalled = true
						_, err := tx.ExecContext(ctx, `insert into audit values('before')`)
						return err
					},
				})
				if err == nil || !strings.Contains(err.Error(), "unsupported") || hookCalled || deleteCalled {
					t.Errorf("effective work not guarded before callbacks: err=%v hook=%v delete=%v", err, hookCalled, deleteCalled)
				}
				assertDependencyRows(t, dst, 1, "old")
				assertIntegrityCount(t, dst, `select count(*) from audit`, 0)
				if !bytes.Equal(before, compositionInputs(t, current, plan)) || !reflect.DeepEqual(returned, plan) {
					t.Fatal("preflight changed caller Current/Plan or returned planning metadata")
				}
			})
		}
	}
}

func TestImportCompositionEmptyCurrentUsesOnlyCustomDelete(t *testing.T) {
	dst := exportTestDB(t, `create table things(id integer primary key, value text);
create table children(id integer primary key, parent_id integer references things(id) on delete cascade);
create table audit(event text);
insert into things values(1,'old'); insert into children values(1,1);`)
	current := Manifest{Version: 1, Tables: []TableManifest{{Name: "things"}}}
	plan := ImportPlan{Tables: []TableImportPlan{{
		Table: TableManifest{Name: "things", File: "planning-only.jsonl.gz"}, Mode: TableImportReplace,
	}}}
	before := compositionInputs(t, current, plan)
	deleted := false
	_, returned, err := ImportIncremental(context.Background(), IncrementalImportOptions{
		DB: dst, Current: current, Plan: plan,
		DeleteTable: func(context.Context, *sql.Tx, string) error { deleted = true; return nil },
		BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `insert into audit values('before')`)
			return err
		},
	})
	if err != nil || !deleted {
		t.Fatalf("unused row callback required for truly empty Current: deleted=%v err=%v", deleted, err)
	}
	assertDependencyRows(t, dst, 1, "old")
	assertIntegrityCount(t, dst, `select count(*) from audit`, 1)
	if !bytes.Equal(before, compositionInputs(t, current, plan)) || !reflect.DeepEqual(returned, plan) {
		t.Fatal("effective work changed caller inputs or returned planning metadata")
	}
}

func TestImportCompositionCustomCallbacksPreserveInputs(t *testing.T) {
	src := exportTestDB(t, `create table things(id integer primary key, value text); insert into things values(1,'new');`)
	root := t.TempDir()
	current, err := Export(context.Background(), ExportOptions{DB: src, RootDir: root, Tables: []string{"things"}})
	if err != nil {
		t.Fatal(err)
	}
	current.Tables[0].Files = nil
	plan := ImportPlan{Tables: []TableImportPlan{{Table: TableManifest{Name: "things"}, Mode: TableImportReplace}}}
	before := compositionInputs(t, current, plan)
	dst := exportTestDB(t, `create table things(id integer primary key, value text);
create table children(id integer primary key, parent_id integer references things(id) on delete cascade);
create table audit(event text);
insert into things values(1,'old'); insert into children values(1,1);`)
	deleted := false
	imported, returned, err := ImportIncremental(context.Background(), IncrementalImportOptions{
		DB: dst, RootDir: root, Current: current, Plan: plan,
		DeleteTable: func(context.Context, *sql.Tx, string) error { deleted = true; return nil },
		ImportRow: func(ctx context.Context, tx *sql.Tx, _ string, row map[string]any) error {
			_, err := tx.ExecContext(ctx, `insert into things(id,value) values(?,?)
on conflict(id) do update set value=excluded.value`, row["id"], row["value"])
			return err
		},
		BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `insert into audit values('before')`)
			return err
		},
	})
	if err != nil || !deleted {
		t.Fatalf("dependency-aware import failed: deleted=%v err=%v", deleted, err)
	}
	assertDependencyRows(t, dst, 1, "new")
	assertIntegrityCount(t, dst, `select count(*) from audit`, 1)
	if !bytes.Equal(before, compositionInputs(t, current, plan)) ||
		!reflect.DeepEqual(returned, plan) || !reflect.DeepEqual(imported, current) {
		t.Fatal("successful effective import mutated inputs or public return metadata")
	}
}

func TestImportCompositionValidatesSelectionBeforeHooks(t *testing.T) {
	for _, invalid := range []string{"stripped metadata", "undeclared", "duplicate file", "duplicate active", "unknown mode"} {
		t.Run(invalid, func(t *testing.T) {
			root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "new"}})
			table := current.Tables[0]
			files := append([]FileManifest(nil), table.FileManifests...)
			mode := TableImportFiles
			switch invalid {
			case "stripped metadata":
				files = []FileManifest{{Path: table.Files[0]}}
			case "undeclared":
				files[0].Path = "unlisted.jsonl.gz"
			case "duplicate file":
				files = append(files, files[0])
			case "unknown mode":
				mode = "unsupported"
			}
			plan := ImportPlan{Tables: []TableImportPlan{{Table: table, Mode: mode, Files: files}}}
			if invalid == "duplicate active" {
				plan.Tables = append(plan.Tables, plan.Tables[0])
			}
			before := compositionInputs(t, current, plan)
			dst := integrityDB(t)
			called := false
			_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
				DB: dst, RootDir: root, Current: current, Plan: plan,
				BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
					called = true
					_, err := tx.ExecContext(ctx, `insert into audit values('before')`)
					return err
				},
			})
			if err == nil || called {
				t.Errorf("invalid selection reached hook: err=%v called=%v", err, called)
			}
			assertIntegrityCount(t, dst, `select count(*) from things where id='old'`, 1)
			assertIntegrityCount(t, dst, `select count(*) from audit`, 0)
			if !bytes.Equal(before, compositionInputs(t, current, plan)) {
				t.Fatal("invalid selection mutated caller inputs")
			}
		})
	}
}

func TestImportCompositionGeneratedShardIntegrity(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge"} {
		t.Run(mode, func(t *testing.T) {
			src := exportTestDB(t, `create table things(id integer primary key, value integer); insert into things values(1,7),(2,8);`)
			root := t.TempDir()
			manifest, err := Export(context.Background(), ExportOptions{DB: src, RootDir: root, Tables: []string{"things"}, MaxShardBytes: 1})
			if err != nil {
				t.Fatal(err)
			}
			files := manifest.Tables[0].Files
			if len(files) != 2 || !strings.HasPrefix(files[1], "tables/.generations/") {
				t.Fatalf("fixture lacks generated physical shards: %v", files)
			}
			dst := exportTestDB(t, `create table things(id integer primary key, value integer); create table audit(event text);`)
			if err := runIntegrityImport(t, mode, manifest, ImportOptions{DB: dst, RootDir: root}); err != nil {
				t.Fatalf("generated manifest rejected: %v", err)
			}
			assertIntegrityCount(t, dst, `select count(*) from things where (id=1 and value=7) or (id=2 and value=8)`, 2)
			mustExec(t, dst, `delete from things; insert into things values(99,99)`)
			path := filepath.Join(root, filepath.FromSlash(files[1]))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[4] ^= 1 // A valid gzip header change, preserving decoded rows.
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			err = runIntegrityImport(t, mode, manifest, ImportOptions{
				DB: dst, RootDir: root,
				BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `insert into audit values('before')`)
					return err
				},
			})
			if err == nil || !strings.Contains(err.Error(), "compressed SHA256 mismatch") {
				t.Fatalf("corrupt generated shard accepted: %v", err)
			}
			assertIntegrityCount(t, dst, `select count(*) from things`, 1)
			assertIntegrityCount(t, dst, `select count(*) from things where id=99 and value=99`, 1)
			assertIntegrityCount(t, dst, `select count(*) from audit`, 0)
		})
	}
}

func TestImportCompositionExactNumbersAndRawCounts(t *testing.T) {
	for _, token := range []string{"9007199254740993.0", "1e100"} {
		t.Run(token, func(t *testing.T) {
			root := rawValuePack(t, "{\"id\":1,\"value\":7}\n{\"id\":2,\"value\":"+token+"}\n{\"id\":3,\"value\":8}\n{}\nnull\n")
			manifest, err := ReadManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "rows.jsonl.gz"))
			if err != nil {
				t.Fatal(err)
			}
			manifest.Tables[0].Rows = 5
			manifest.Tables[0].FileManifests = []FileManifest{{
				Path: "rows.jsonl.gz", Rows: 5, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
			}}
			if err := WriteManifest(root, manifest); err != nil {
				t.Fatal(err)
			}
			dst := exportTestDB(t, `create table things(id integer primary key, value integer); insert into things values(99,99);
create table audit(event text);`)
			var progress ImportProgress
			_, err = Import(context.Background(), ImportOptions{
				DB: dst, RootDir: root,
				BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `insert into audit values('before')`)
					return err
				},
				Filter: func(_ string, row map[string]any) (bool, error) { return row["id"] != float64(3), nil },
				ImportRow: func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
					want := any(float64(7))
					if row["id"] == float64(2) {
						want = int64(9007199254740993)
					}
					if row["value"] != want {
						t.Fatalf("numeric callback changed: %T(%v), want %T(%v)", row["value"], row["value"], want, want)
					}
					return insertRow(ctx, tx, table, row)
				},
				Progress: func(event ImportProgress) { progress = event },
			})
			if token == "1e100" {
				if err == nil {
					t.Fatal("integrity merge bypassed numeric refusal")
				}
				assertIntegrityCount(t, dst, `select count(*) from things where id=99 and value=99`, 1)
				assertIntegrityCount(t, dst, `select count(*) from things`, 1)
				assertIntegrityCount(t, dst, `select count(*) from audit`, 0)
				return
			}
			if err != nil || progress.Phase != "table_done" || progress.Rows != 2 || progress.TotalRows != 5 {
				t.Fatalf("raw/admitted count or import mismatch: %+v, %v", progress, err)
			}
			var value int64
			if err := dst.QueryRow(`select value from things where id=2`).Scan(&value); err != nil || value != 9007199254740993 {
				t.Fatalf("exact numeric binding lost: %d %v", value, err)
			}
			assertIntegrityCount(t, dst, `select count(*) from things`, 2)
		})
	}
}
