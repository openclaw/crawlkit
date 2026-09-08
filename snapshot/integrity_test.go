package snapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestImportIntegrityValidMetadata(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge"} {
		for _, metadata := range []string{"complete", "hash only", "size only", "rows only", "metadata only", "reordered", "aliased Files", "aliased File", "legacy Files", "legacy File"} {
			t.Run(mode+"/"+metadata, func(t *testing.T) {
				root, manifest := integritySnapshot(t,
					[]map[string]any{{"id": "one", "body": "first"}},
					[]map[string]any{{"id": "two", "body": "second"}},
				)
				table := &manifest.Tables[0]
				wantRows := 2
				switch metadata {
				case "hash only":
					for i := range table.FileManifests {
						table.FileManifests[i].Size = 0
					}
				case "size only":
					for i := range table.FileManifests {
						table.FileManifests[i].SHA256 = ""
					}
				case "rows only":
					for i := range table.FileManifests {
						table.FileManifests[i].Size = 0
						table.FileManifests[i].SHA256 = ""
					}
				case "metadata only":
					table.Files = nil
				case "reordered":
					table.FileManifests[0], table.FileManifests[1] = table.FileManifests[1], table.FileManifests[0]
				case "aliased Files":
					table.Files[0] = "tables/things/x/../000000.jsonl.gz"
				case "aliased File":
					table.File = "tables/things/x/../000000.jsonl.gz"
					table.Files = table.Files[:1]
					table.FileManifests = table.FileManifests[:1]
					table.Rows = 1
					wantRows = 1
				case "legacy Files":
					table.FileManifests = nil
					table.Rows = 0
				case "legacy File":
					table.File = table.Files[1]
					table.Files = nil
					table.FileManifests = nil
					table.Rows = 0
					wantRows = 1
				}
				db := integrityDB(t)
				if err := runIntegrityImport(t, mode, manifest, ImportOptions{DB: db, RootDir: root}); err != nil {
					t.Fatal(err)
				}
				assertIntegrityCount(t, db, `select count(*) from things where id != 'old'`, wantRows)
				wantOld := 0
				if mode == "merge" {
					wantOld = 1
				}
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old'`, wantOld)
			})
		}
	}
}

func TestImportIntegrityCountsDecodedRowsBeforeCallbacks(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge"} {
		t.Run(mode, func(t *testing.T) {
			root, manifest := integritySnapshot(t, []map[string]any{
				{"id": "one", "body": "keep"},
				{},
				nil,
				{"id": "filtered", "body": "skip"},
				{"id": "ignored", "body": "skip"},
			}, nil)
			db := integrityDB(t)
			filterCalls, importCalls := 0, 0
			var progress []ImportProgress
			err := runIntegrityImport(t, mode, manifest, ImportOptions{
				DB: db, RootDir: root,
				Filter: func(_ string, row map[string]any) (bool, error) {
					filterCalls++
					return row["id"] != "filtered", nil
				},
				ImportRow: func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
					importCalls++
					if row["id"] == "ignored" {
						return nil
					}
					return insertRow(ctx, tx, table, row)
				},
				Progress: func(event ImportProgress) { progress = append(progress, event) },
			})
			if err != nil {
				t.Fatal(err)
			}
			if filterCalls != 3 || importCalls != 2 {
				t.Fatalf("filter calls = %d, import calls = %d", filterCalls, importCalls)
			}
			assertIntegrityCount(t, db, `select count(*) from things where id != 'old'`, 1)
			if got := progress[len(progress)-1]; got.Phase != "table_done" || got.Rows != 2 || got.TotalRows != 5 {
				t.Fatalf("table progress = %+v", got)
			}
		})
	}
}

func TestImportIntegrityRollsBackBadLaterFile(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge"} {
		for _, corruption := range []struct {
			name string
			want string
		}{
			{"hash", "compressed SHA256 mismatch"},
			{"size small", "compressed size mismatch"},
			{"size large", "compressed size mismatch"},
			{"negative size", "compressed size mismatch"},
			{"rows zero", "row count mismatch"},
			{"rows large", "row count mismatch"},
			{"truncated gzip", "unexpected EOF"},
			{"gzip checksum", "invalid checksum"},
			{"legacy truncated gzip", "unexpected EOF"},
		} {
			t.Run(mode+"/"+corruption.name, func(t *testing.T) {
				root, manifest := integritySnapshot(t,
					[]map[string]any{{"id": "one", "body": "first"}},
					[]map[string]any{{"id": "two", "body": "second"}},
				)
				bad := &manifest.Tables[0].FileManifests[1]
				switch corruption.name {
				case "hash":
					bad.SHA256 = strings.Repeat("0", 64)
				case "size small":
					bad.Size--
				case "size large":
					bad.Size++
				case "negative size":
					bad.Size = -1
				case "rows zero":
					bad.Rows = 0
					manifest.Tables[0].Rows = 1
				case "rows large":
					bad.Rows = 2
					manifest.Tables[0].Rows = 3
				default:
					path := filepath.Join(root, filepath.FromSlash(bad.Path))
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if corruption.name == "gzip checksum" {
						data[len(data)-8] ^= 1
					} else {
						data = data[:len(data)-1]
					}
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
					if corruption.name == "legacy truncated gzip" {
						manifest.Tables[0].FileManifests = nil
					}
				}
				db := integrityDB(t)
				beforeCalled, deleteCalled, afterCalled := false, false, false
				var imported []string
				var progress []ImportProgress
				err := runIntegrityImport(t, mode, manifest, ImportOptions{
					DB: db, RootDir: root,
					BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
						beforeCalled = true
						_, err := tx.ExecContext(ctx, `insert into audit values ('before')`)
						return err
					},
					DeleteTable: func(ctx context.Context, tx *sql.Tx, table string) error {
						deleteCalled = true
						if _, err := tx.ExecContext(ctx, `insert into audit values ('delete')`); err != nil {
							return err
						}
						return deleteImportTable(ctx, tx, table, nil)
					},
					ImportRow: func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
						imported = append(imported, row["id"].(string))
						if _, err := tx.ExecContext(ctx, `insert into audit values ('row')`); err != nil {
							return err
						}
						return insertRow(ctx, tx, table, row)
					},
					AfterImport: func(ctx context.Context, tx *sql.Tx) error {
						afterCalled = true
						_, err := tx.ExecContext(ctx, `insert into audit values ('after')`)
						return err
					},
					Progress: func(event ImportProgress) { progress = append(progress, event) },
				})
				if err == nil || !strings.Contains(err.Error(), corruption.want) {
					t.Fatalf("error = %v, want %q", err, corruption.want)
				}
				if !strings.Contains(err.Error(), manifest.Tables[0].Files[1]) {
					t.Fatalf("error does not identify bad shard: %v", err)
				}
				if !beforeCalled || deleteCalled != (mode != "merge") || afterCalled {
					t.Fatalf("hooks: before=%t delete=%t after=%t", beforeCalled, deleteCalled, afterCalled)
				}
				if len(imported) == 0 || imported[0] != "one" {
					t.Fatalf("first file was not imported before failure: %v", imported)
				}
				done := 0
				for _, event := range progress {
					if event.Phase == "file_done" {
						done++
						if event.File != manifest.Tables[0].Files[0] {
							t.Fatalf("corrupt file reported done: %+v", event)
						}
					}
					if event.Phase == "table_done" {
						t.Fatalf("failed table reported done: %+v", event)
					}
				}
				if done != 1 {
					t.Fatalf("completed files = %d", done)
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
				assertIntegrityCount(t, db, `select count(*) from audit`, 0)
			})
		}
	}
}

func TestImportIntegrityRejectsInconsistentPaths(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge", "skip"} {
		for _, mismatch := range []string{"missing entry", "extra entry", "missing path", "wrong path", "duplicate metadata", "duplicate file", "conflicting File"} {
			t.Run(mode+"/"+mismatch, func(t *testing.T) {
				root, manifest := integritySnapshot(t,
					[]map[string]any{{"id": "one", "body": "first"}},
					[]map[string]any{{"id": "two", "body": "second"}},
				)
				table := &manifest.Tables[0]
				switch mismatch {
				case "missing entry":
					table.FileManifests = table.FileManifests[:1]
				case "extra entry":
					table.Files = table.Files[:1]
				case "missing path":
					table.FileManifests[1].Path = ""
				case "wrong path":
					table.FileManifests[1].Path = "other.jsonl.gz"
				case "duplicate metadata":
					table.FileManifests[1] = table.FileManifests[0]
				case "duplicate file":
					table.Files[1] = table.Files[0]
				case "conflicting File":
					table.File = "other.jsonl.gz"
				}
				db := integrityDB(t)
				err := runIntegrityImport(t, mode, manifest, ImportOptions{DB: db, RootDir: root})
				if err == nil || !strings.Contains(err.Error(), "table things:") {
					t.Fatalf("expected manifest metadata error, got %v", err)
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old'`, 1)
			})
		}
	}
}

func TestImportIntegrityLegacyPartialPlan(t *testing.T) {
	for _, field := range []string{"File", "Files"} {
		t.Run(field, func(t *testing.T) {
			root, manifest := integritySnapshot(t,
				[]map[string]any{{"id": "one", "body": "first"}},
				[]map[string]any{{"id": "two", "body": "second"}},
			)
			table := &manifest.Tables[0]
			table.FileManifests = nil
			table.Rows = 0
			if field == "File" {
				table.File = table.Files[1]
				table.Files = nil
			}
			files := tableFileManifests(*table)
			plan := ImportPlan{Tables: []TableImportPlan{{
				Table: *table,
				Mode:  TableImportFiles,
				Files: files[len(files)-1:],
			}}}
			db := integrityDB(t)
			if _, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
				DB: db, RootDir: root, Current: manifest, Plan: plan,
			}); err != nil {
				t.Fatal(err)
			}
			assertIntegrityCount(t, db, `select count(*) from things`, 2)
			assertIntegrityCount(t, db, `select count(*) from things where id in ('old', 'two')`, 2)
		})
	}
}

func TestImportIntegrityRejectsInconsistentRows(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge", "skip"} {
		for _, mismatch := range []string{"missing last shard", "zero total", "small total", "large total", "negative total", "negative file rows", "overflow"} {
			t.Run(mode+"/"+mismatch, func(t *testing.T) {
				root, manifest := integritySnapshot(t,
					[]map[string]any{{"id": "one", "body": "first"}},
					[]map[string]any{{"id": "two", "body": "second"}},
				)
				table := &manifest.Tables[0]
				switch mismatch {
				case "missing last shard":
					table.Files = table.Files[:1]
					table.FileManifests = table.FileManifests[:1]
				case "zero total":
					table.Rows = 0
				case "small total":
					table.Rows = 1
				case "large total":
					table.Rows = 3
				case "negative total":
					table.Rows = -1
				case "negative file rows":
					table.FileManifests[1].Rows = -1
				case "overflow":
					table.Rows = int(^uint(0) >> 1)
					table.FileManifests[0].Rows = table.Rows
				}
				db := integrityDB(t)
				afterCalled := false
				err := runIntegrityImport(t, mode, manifest, ImportOptions{
					DB: db, RootDir: root,
					BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, `insert into audit values ('before')`)
						return err
					},
					AfterImport: func(context.Context, *sql.Tx) error {
						afterCalled = true
						return nil
					},
				})
				if err == nil || !strings.Contains(err.Error(), "table things:") || !strings.Contains(err.Error(), "row count") {
					t.Fatalf("expected table row count error, got %v", err)
				}
				if afterCalled {
					t.Fatal("AfterImport called with invalid table row count")
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
				assertIntegrityCount(t, db, `select count(*) from audit`, 0)
			})
		}
	}
}

func TestImportIntegrityRejectsStrippedPlannedMetadata(t *testing.T) {
	root, manifest := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
	table := manifest.Tables[0]
	plan := ImportPlan{Tables: []TableImportPlan{{
		Table: table,
		Mode:  TableImportFiles,
		Files: []FileManifest{{Path: table.Files[0]}},
	}}}
	db := integrityDB(t)
	_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
		DB: db, RootDir: root, Current: manifest, Plan: plan,
	})
	if err == nil || !strings.Contains(err.Error(), "inconsistent planned file manifest") {
		t.Fatalf("expected planned metadata error, got %v", err)
	}
	assertIntegrityCount(t, db, `select count(*) from things where id = 'old'`, 1)
}

func TestImportIntegrityBindsSuppliedPlanToCurrentManifest(t *testing.T) {
	for _, mode := range []TableImportMode{TableImportReplace, TableImportFiles} {
		for _, metadata := range []string{"stripped", "forged"} {
			t.Run(string(mode)+"/"+metadata, func(t *testing.T) {
				root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
				table := current.Tables[0]
				path := filepath.Join(root, filepath.FromSlash(table.Files[0]))
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// Changing the gzip mtime preserves rows and size but changes its hash.
				data[4] ^= 1
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				files := []FileManifest{{Path: table.Files[0]}}
				table.FileManifests = nil
				table.Rows = 0
				if metadata == "forged" {
					files[0] = current.Tables[0].FileManifests[0]
					files[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
					table.FileManifests = files
					table.Rows = files[0].Rows
				}
				plan := ImportPlan{Tables: []TableImportPlan{{Table: table, Mode: mode, Files: files}}}
				db := integrityDB(t)
				afterCalled := false
				_, _, err = ImportIncremental(context.Background(), IncrementalImportOptions{
					DB: db, RootDir: root, Current: current, Plan: plan,
					BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, `insert into audit values ('before')`)
						return err
					},
					AfterImport: func(context.Context, *sql.Tx) error {
						afterCalled = true
						return nil
					},
				})
				want := "compressed SHA256 mismatch"
				if mode == TableImportFiles {
					want = "inconsistent planned file manifest"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want %q", err, want)
				}
				if afterCalled {
					t.Fatal("AfterImport called for a corrupt supplied plan")
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
				assertIntegrityCount(t, db, `select count(*) from audit`, 0)
				if metadata == "stripped" && len(plan.Tables[0].Table.FileManifests) != 0 {
					t.Fatal("import mutated the caller's plan metadata")
				}
			})
		}
	}
}

func TestImportIntegrityRejectsPlanTableAbsentFromCurrent(t *testing.T) {
	root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
	plan := PlanMergeImport(Manifest{Version: current.Version}, current)
	plan.Tables[0].Table.Name = "missing"
	db := integrityDB(t)
	_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
		DB: db, RootDir: root, Current: current, Plan: plan,
	})
	if err == nil || !strings.Contains(err.Error(), `planned table "missing" is not in the current manifest`) {
		t.Fatalf("expected missing plan table error, got %v", err)
	}
	assertIntegrityCount(t, db, `select count(*) from things where id = 'old'`, 1)
}

func TestImportIntegrityRejectsShadowedCurrentTable(t *testing.T) {
	root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
	legacy := current.Tables[0]
	legacy.FileManifests = nil
	current.Tables = append(current.Tables, legacy)
	db := integrityDB(t)
	_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
		DB: db, RootDir: root, Current: current, Previous: Manifest{Version: current.Version},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate table "things" in the current manifest`) {
		t.Fatalf("expected duplicate current table error, got %v", err)
	}
	assertIntegrityCount(t, db, `select count(*) from things where id = 'old'`, 1)
}

func TestImportIntegrityRejectsInvalidLegacyPlanPaths(t *testing.T) {
	for _, field := range []string{"File", "Files"} {
		for _, invalid := range []string{"undeclared", "duplicate"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				root, current := integritySnapshot(t,
					[]map[string]any{{"id": "one", "body": "first"}},
					[]map[string]any{{"id": "two", "body": "second"}},
				)
				table := &current.Tables[0]
				knownPath := table.Files[0]
				table.FileManifests = nil
				table.Rows = 0
				if field == "File" {
					table.File = knownPath
					table.Files = nil
				}
				selected := []FileManifest{{Path: knownPath}}
				if invalid == "undeclared" {
					rel := "tables/things/undeclared.jsonl.gz"
					writeGzipJSONL(t, filepath.Join(root, filepath.FromSlash(rel)), map[string]any{"id": "rogue", "body": "undeclared"})
					selected = append(selected, FileManifest{Path: rel})
				} else {
					selected = append(selected, selected[0])
				}
				plan := ImportPlan{Tables: []TableImportPlan{{
					Table: *table, Mode: TableImportFiles, Files: selected,
				}}}
				db := integrityDB(t)
				afterCalled := false
				_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
					DB: db, RootDir: root, Current: current, Plan: plan,
					BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, `insert into audit values ('before')`)
						return err
					},
					AfterImport: func(context.Context, *sql.Tx) error {
						afterCalled = true
						return nil
					},
				})
				if err == nil || !strings.Contains(err.Error(), "inconsistent planned file manifest") {
					t.Fatalf("expected invalid legacy plan path error, got %v", err)
				}
				if afterCalled {
					t.Fatal("AfterImport called with invalid legacy plan paths")
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
				assertIntegrityCount(t, db, `select count(*) from audit`, 0)
			})
		}
	}
}

func TestImportIntegrityRejectsNegativeLegacyRows(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge", "skip"} {
		for _, field := range []string{"File", "Files", "no files"} {
			t.Run(mode+"/"+field, func(t *testing.T) {
				root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
				table := &current.Tables[0]
				table.FileManifests = nil
				table.Rows = -1
				if field == "File" {
					table.File = table.Files[0]
					table.Files = nil
				} else if field == "no files" {
					table.Files = nil
				}
				db := integrityDB(t)
				err := runIntegrityImport(t, mode, current, ImportOptions{DB: db, RootDir: root})
				if err == nil || !strings.Contains(err.Error(), "negative row count") {
					t.Fatalf("expected negative legacy row count error, got %v", err)
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
			})
		}
	}
}

func TestImportIntegrityRejectsDuplicateActivePlans(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, first := range []TableImportMode{TableImportFiles, TableImportReplace} {
			for _, second := range []TableImportMode{TableImportFiles, TableImportReplace} {
				t.Run(fmt.Sprintf("legacy=%t/%s/%s", legacy, first, second), func(t *testing.T) {
					root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
					if legacy {
						current.Tables[0].FileManifests = nil
						current.Tables[0].Rows = 0
					}
					table := current.Tables[0]
					files := tableFileManifests(table)
					plan := ImportPlan{Tables: []TableImportPlan{
						{Table: table, Mode: first, Files: files},
						{Table: table, Mode: second, Files: files},
					}}
					db := integrityDB(t)
					beforeCalled := false
					_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
						DB: db, RootDir: root, Current: current, Plan: plan,
						BeforeImport: func(context.Context, *sql.Tx) error {
							beforeCalled = true
							return nil
						},
					})
					if err == nil || !strings.Contains(err.Error(), `duplicate active plan for table "things"`) {
						t.Fatalf("expected duplicate active plan error, got %v", err)
					}
					if beforeCalled {
						t.Fatal("BeforeImport called before rejecting duplicate active plans")
					}
					assertIntegrityCount(t, db, `select count(*) from things`, 1)
					assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
				})
			}
		}
	}
}

func TestImportIntegrityRejectsAliasedManifestPaths(t *testing.T) {
	for _, mode := range []string{"full", "replace", "merge", "skip"} {
		for _, metadata := range []string{"modern", "legacy", "Files only"} {
			t.Run(mode+"/"+metadata, func(t *testing.T) {
				root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
				table := &current.Tables[0]
				alias := table.FileManifests[0]
				alias.Path = "tables/things/x/../000000.jsonl.gz"
				table.Files = append(table.Files, alias.Path)
				if metadata == "legacy" {
					table.FileManifests = nil
					table.Rows = 0
				} else if metadata == "modern" {
					table.FileManifests = append(table.FileManifests, alias)
					table.Rows += alias.Rows
				}
				db := integrityDB(t)
				afterCalled := false
				err := runIntegrityImport(t, mode, current, ImportOptions{
					DB: db, RootDir: root,
					BeforeImport: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, `insert into audit values ('before')`)
						return err
					},
					AfterImport: func(context.Context, *sql.Tx) error {
						afterCalled = true
						return nil
					},
				})
				if err == nil || !strings.Contains(err.Error(), "table things:") {
					t.Fatalf("expected duplicate shard path error, got %v", err)
				}
				if afterCalled {
					t.Fatal("AfterImport called for duplicate shard paths")
				}
				assertIntegrityCount(t, db, `select count(*) from things`, 1)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
				assertIntegrityCount(t, db, `select count(*) from audit`, 0)
			})
		}
	}
}

func TestImportIntegrityAliasedPlanPaths(t *testing.T) {
	for _, metadata := range []string{"modern", "legacy File", "legacy Files"} {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate=%t", metadata, duplicate), func(t *testing.T) {
				root, current := integritySnapshot(t, []map[string]any{{"id": "one", "body": "first"}})
				table := &current.Tables[0]
				if metadata != "modern" {
					table.FileManifests = nil
					table.Rows = 0
				}
				if metadata == "legacy File" {
					table.File = table.Files[0]
					table.Files = nil
				}
				original := tableFileManifests(*table)[0]
				alias := original
				alias.Path = "tables/things/x/../000000.jsonl.gz"
				selected := []FileManifest{alias}
				if duplicate {
					selected = append(selected, original)
				}
				plan := ImportPlan{Tables: []TableImportPlan{{
					Table: *table, Mode: TableImportFiles, Files: selected,
				}}}
				db := integrityDB(t)
				_, _, err := ImportIncremental(context.Background(), IncrementalImportOptions{
					DB: db, RootDir: root, Current: current, Plan: plan,
					ImportRow: func(ctx context.Context, tx *sql.Tx, table string, row map[string]any) error {
						_, err := tx.ExecContext(ctx, `insert into audit values ('row')`)
						return err
					},
				})
				wantRows := 1
				if duplicate {
					wantRows = 0
					if err == nil || !strings.Contains(err.Error(), "inconsistent planned file manifest") {
						t.Fatalf("expected duplicate planned shard error, got %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				assertIntegrityCount(t, db, `select count(*) from audit`, wantRows)
				assertIntegrityCount(t, db, `select count(*) from things where id = 'old' and body = 'original'`, 1)
			})
		}
	}
}

func integritySnapshot(t *testing.T, shards ...[]map[string]any) (string, Manifest) {
	t.Helper()
	root := t.TempDir()
	table := TableManifest{Name: "things", Columns: []string{"id", "body"}}
	for i, rows := range shards {
		rel := fmt.Sprintf("tables/things/%06d.jsonl.gz", i)
		path := filepath.Join(root, filepath.FromSlash(rel))
		writeGzipJSONL(t, path, rows...)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		table.Files = append(table.Files, rel)
		table.FileManifests = append(table.FileManifests, FileManifest{
			Path: rel, Rows: len(rows), Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
		})
		table.Rows += len(rows)
	}
	return root, Manifest{Version: 1, Tables: []TableManifest{table}}
}

func integrityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{
		Path: filepath.Join(t.TempDir(), "dst.db"),
		Schema: `create table things(id text primary key, body text not null);
create table audit(event text not null);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db.DB(), `insert into things values ('old', 'original')`)
	return db.DB()
}

func runIntegrityImport(t *testing.T, mode string, manifest Manifest, opts ImportOptions) error {
	t.Helper()
	ctx := context.Background()
	if mode == "full" {
		if err := WriteManifest(opts.RootDir, manifest); err != nil {
			t.Fatal(err)
		}
		_, err := Import(ctx, opts)
		return err
	}
	previous := Manifest{Version: manifest.Version}
	plan := PlanIncrementalImport(previous, manifest)
	if mode == "merge" {
		plan = PlanMergeImport(previous, manifest)
	} else if mode == "skip" {
		previous = manifest
		plan = PlanIncrementalImport(previous, manifest)
	}
	_, _, err := ImportIncremental(ctx, IncrementalImportOptions{
		DB: opts.DB, RootDir: opts.RootDir, Previous: previous, Current: manifest, Plan: plan,
		DeleteTable: opts.DeleteTable, Filter: opts.Filter, ImportRow: opts.ImportRow, Progress: opts.Progress,
		BeforeImport: opts.BeforeImport, AfterImport: opts.AfterImport,
	})
	return err
}

func assertIntegrityCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d, want %d", query, got, want)
	}
}
