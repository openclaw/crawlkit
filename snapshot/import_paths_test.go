package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openclaw/crawlkit/store"
)

func TestImportPreservesLiteralPaths(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		mode := "full"
		if incremental {
			mode = "incremental"
		}
		t.Run(mode, func(t *testing.T) {
			for _, name := range []string{"nested", "dot", "dot-slash", "empty-root", "root-space", "shard-space", "legacy-shard-space"} {
				t.Run(name, func(t *testing.T) {
					if runtime.GOOS == "windows" && (name == "root-space" || name == "shard-space" || name == "legacy-shard-space") {
						t.Skip("Windows normalizes trailing spaces in filesystem names")
					}
					ctx := context.Background()
					base := t.TempDir()
					root := filepath.Join(base, "archive")
					rel := "tables/things/000001.jsonl.gz"
					if name == "root-space" {
						root += " "
					}
					if name == "shard-space" || name == "legacy-shard-space" {
						rel = " tables/things/000001.jsonl.gz "
					}
					write := func(path, id string) {
						t.Helper()
						if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
							t.Fatal(err)
						}
						writeGzipJSONL(t, path, map[string]any{"id": id})
					}
					write(filepath.Join(root, rel), "intended")
					if name == "root-space" {
						write(filepath.Join(base, "archive", rel), "foreign")
					}
					if name == "shard-space" || name == "legacy-shard-space" {
						write(filepath.Join(root, "tables/things/000001.jsonl.gz"), "foreign")
					}
					table := TableManifest{Name: "things", Files: []string{rel}, Columns: []string{"id"}, Rows: 1}
					if name == "legacy-shard-space" {
						table.Files = nil
						table.File = rel
					}
					current := Manifest{Version: 1, Tables: []TableManifest{table}}
					if err := WriteManifest(root, current); err != nil {
						t.Fatal(err)
					}
					switch name {
					case "dot", "dot-slash", "empty-root":
						t.Chdir(root)
						root = map[string]string{"dot": ".", "dot-slash": "./", "empty-root": ""}[name]
					}
					dst, err := store.Open(ctx, store.Options{Path: ":memory:", Schema: "create table things(id text primary key)"})
					if err != nil {
						t.Fatal(err)
					}
					defer dst.Close()
					if incremental {
						_, _, err = ImportIncremental(ctx, IncrementalImportOptions{DB: dst.DB(), RootDir: root, Previous: Manifest{Version: 1}, Current: current})
					} else {
						_, err = Import(ctx, ImportOptions{DB: dst.DB(), RootDir: root})
					}
					if err != nil {
						t.Fatal(err)
					}
					var id string
					if err := dst.DB().QueryRowContext(ctx, "select id from things").Scan(&id); err != nil {
						t.Fatal(err)
					}
					if id != "intended" {
						t.Fatalf("imported %q, want intended root and shard", id)
					}
				})
			}
		})
	}
}
