package cache

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/store"
)

func TestSnapshotSQLiteReusedDestinationDropsStaleWAL(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open(ctx, store.Options{Path: source, Schema: `create table items(value text); insert into items values('old');`})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	destination := t.TempDir()
	sentinel := filepath.Join(destination, "unrelated")
	if err := os.WriteFile(sentinel, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := SQLiteSnapshotOptions{SourcePath: source, DestinationDir: destination, Name: "archive.db"}
	first, err := SnapshotSQLite(opts)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(first.Path + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("fixture lacks uncheckpointed WAL: %v, %v", wal, err)
	}
	if _, err := st.DB().ExecContext(ctx, `update items set value='new'; pragma wal_checkpoint(TRUNCATE);`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("source WAL still exists: %v", err)
	}
	second, err := SnapshotSQLite(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(second.Path + suffix); !os.IsNotExist(err) {
			t.Errorf("stale destination sidecar %s retained: %v", suffix, err)
		}
	}
	ro, err := store.OpenReadOnly(ctx, second.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var value string
	if err := ro.DB().QueryRowContext(ctx, `select value from items`).Scan(&value); err != nil || value != "new" {
		t.Fatalf("reused snapshot value = %q, error = %v", value, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "retain" {
		t.Fatalf("unrelated destination file changed: %q, %v", data, err)
	}
}

func TestSnapshotSQLiteFailurePreservesPreviousBundle(t *testing.T) {
	for _, failingSuffix := range []string{"-wal", "-shm"} {
		t.Run(failingSuffix, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source.db")
			destination := t.TempDir()
			target := filepath.Join(destination, "archive.db")
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.WriteFile(target+suffix, []byte("old"+suffix), 0o600); err != nil {
					t.Fatal(err)
				}
				data := []byte("new")
				if suffix == failingSuffix {
					data = []byte("exceeds maximum")
				}
				if err := os.WriteFile(source+suffix, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SnapshotSQLite(SQLiteSnapshotOptions{
				SourcePath: source, DestinationDir: destination, Name: "archive.db", MaxFileBytes: 8,
			}); err == nil {
				t.Fatal("expected sidecar size failure")
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				got, err := os.ReadFile(target + suffix)
				if err != nil || string(got) != "old"+suffix {
					t.Errorf("previous %s changed: %q, %v", suffix, got, err)
				}
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 3 {
				t.Fatalf("staging files leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestPublishSQLiteBundleRollsBackFailedMainPromotion(t *testing.T) {
	target := filepath.Join(t.TempDir(), "archive.db")
	staged := filepath.Join(t.TempDir(), "archive.db")
	previous := filepath.Join(t.TempDir(), "archive.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(target+suffix, []byte("old"+suffix), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(staged+"-wal", []byte("new WAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	retained, err := publishSQLiteBundle(staged, previous, target, map[string]bool{"": true, "-wal": true})
	if err == nil || retained {
		t.Fatalf("failed main promotion: retained=%v, error=%v", retained, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(target + suffix)
		if err != nil || string(data) != "old"+suffix {
			t.Fatalf("old bundle not restored at %s: %q, %v", suffix, data, err)
		}
	}
}

func TestSnapshotSQLitePreservesExistingAliasCompatibility(t *testing.T) {
	for _, kind := range []string{"same path", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.db")
			if err := os.WriteFile(source, []byte("source bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			name := "source.db"
			if kind == "hardlink" {
				name = "alias.db"
				if err := os.Link(source, filepath.Join(dir, name)); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			}
			snapshot, err := SnapshotSQLite(SQLiteSnapshotOptions{SourcePath: source, DestinationDir: dir, Name: name})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(source)
			if err != nil || string(data) != "source bytes" {
				t.Fatalf("source changed: %q, %v", data, err)
			}
			data, err = os.ReadFile(snapshot.Path)
			if err != nil || string(data) != "source bytes" {
				t.Fatalf("snapshot content: %q, %v", data, err)
			}
			if kind == "hardlink" {
				after, err := os.Stat(source)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("distinct source inode replaced: %v", err)
				}
			}
		})
	}
}

func TestSnapshotSQLitePreservesLiteralPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Win32 aliases trailing spaces; covered by the rejection test")
	}
	for _, part := range []string{"source", "destination"} {
		t.Run(part, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			sourceName, destinationName := "source.db", "capture"
			if part == "source" {
				sourceName += " "
			} else {
				destinationName += " "
			}
			fixtureNames := []string{sourceName}
			if part == "source" {
				fixtureNames = append(fixtureNames, "source.db")
			}
			for _, name := range fixtureNames {
				uri := (&url.URL{Scheme: "file", Path: filepath.Join(root, name)}).String()
				db, err := sql.Open("sqlite", uri)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := db.ExecContext(ctx, `create table items(value text); insert into items values(?);`, name)
				closeErr := db.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("fixture database: write=%v close=%v", writeErr, closeErr)
				}
			}
			source := filepath.Join(root, sourceName)
			destination := filepath.Join(root, destinationName)
			neighbor := filepath.Join(root, "capture", "archive.db")
			if part == "destination" {
				if err := os.MkdirAll(filepath.Dir(neighbor), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(neighbor, []byte("preserved neighbor"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			snap, err := SnapshotSQLite(SQLiteSnapshotOptions{SourcePath: source, DestinationDir: destination, Name: "archive.db"})
			if err != nil {
				t.Fatal(err)
			}
			wantPath := filepath.Join(destination, "archive.db")
			if snap.SourcePath != source || snap.Path != wantPath {
				t.Errorf("snapshot paths = %q, %q; want %q, %q", snap.SourcePath, snap.Path, source, wantPath)
			}
			if part == "destination" {
				if body, err := os.ReadFile(neighbor); err != nil || string(body) != "preserved neighbor" {
					t.Errorf("neighbor changed: bytes=%d read error=%v", len(body), err)
				}
			}
			ro, err := store.OpenReadOnly(ctx, wantPath)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			var value string
			if err := ro.DB().QueryRowContext(ctx, `select value from items`).Scan(&value); err != nil || value != sourceName {
				t.Fatalf("captured value = %q, %v; want %q", value, err, sourceName)
			}
		})
	}
}

func TestSnapshotSQLiteRejectsAmbiguousWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Win32 path aliases")
	}
	for _, paths := range [][2]string{{"source.db ", "capture"}, {"source.db.", "capture"}, {"source.db", "capture "}, {"source.db", "capture."}} {
		t.Run(paths[0]+"_"+paths[1], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.db")
			target := filepath.Join(root, "capture", "archive.db")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{source, target} {
				if err := os.WriteFile(path, []byte("preserved"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := SnapshotSQLite(SQLiteSnapshotOptions{SourcePath: filepath.Join(root, paths[0]), DestinationDir: filepath.Join(root, paths[1]), Name: "archive.db"})
			if err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Errorf("expected ambiguous-path rejection, got %v", err)
			}
			for _, path := range []string{source, target} {
				if body, err := os.ReadFile(path); err != nil || string(body) != "preserved" {
					t.Errorf("neighbor changed: bytes=%d read error=%v", len(body), err)
				}
			}
		})
	}
}

func TestSQLiteModifiedAfter(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, suffix string
		delta        time.Duration
		want         bool
	}{
		{"all_older", "", -time.Hour, false},
		{"main_equal", "", 0, false},
		{"main_newer", "", time.Hour, true},
		{"wal_equal", "-wal", 0, false},
		{"wal_newer", "-wal", time.Hour, true},
		{"shm_equal", "-shm", 0, false},
		{"shm_newer", "-shm", time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.db")
			for _, part := range []string{"", "-wal", "-shm"} {
				if err := os.WriteFile(path+part, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				modified := cutoff.Add(-time.Hour)
				if part == tc.suffix {
					modified = cutoff.Add(tc.delta)
				}
				if err := os.Chtimes(path+part, modified, modified); err != nil {
					t.Fatal(err)
				}
			}
			if got := SQLiteModifiedAfter(path, cutoff); got != tc.want {
				t.Fatalf("modified after cutoff = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("invalid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "archive.db")
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			path   string
			cutoff time.Time
		}{{"", cutoff}, {path, time.Time{}}, {path + ".missing", cutoff}} {
			if SQLiteModifiedAfter(tc.path, tc.cutoff) {
				t.Errorf("invalid modification check returned true: path=%q cutoff=%v", tc.path, tc.cutoff)
			}
		}
	})
}

func TestSQLiteModifiedAfterPreservesLiteralPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Win32 aliases trailing spaces; covered by the rejection test")
	}
	cutoff := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for name, suffix := range map[string]string{"main": "", "wal": "-wal", "shm": "-shm"} {
		for _, tc := range []struct {
			name string
			want bool
		}{{"literal_newer", true}, {"neighbor_newer", false}} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				neighbor := filepath.Join(t.TempDir(), "archive.db")
				literal := neighbor + " "
				for _, path := range []string{literal, neighbor} {
					if err := os.WriteFile(path+suffix, []byte("fixture"), 0o600); err != nil {
						t.Fatal(err)
					}
					modified := cutoff.Add(-time.Hour)
					if (path == literal) == tc.want {
						modified = cutoff.Add(time.Hour)
					}
					if err := os.Chtimes(path+suffix, modified, modified); err != nil {
						t.Fatal(err)
					}
				}
				if got := SQLiteModifiedAfter(literal, cutoff); got != tc.want {
					t.Fatalf("literal bundle modified after cutoff = %v, want %v", got, tc.want)
				}
			})
		}
	}
}

func TestSQLiteModifiedAfterRejectsWindowsAliases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Win32 path aliases")
	}
	root := t.TempDir()
	path := filepath.Join(root, "folder", "archive.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	modified := cutoff.Add(time.Hour)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	if !SQLiteModifiedAfter(path, cutoff) {
		t.Fatal("canonical database should be newer than cutoff")
	}
	for _, alias := range []string{path + " ", path + ".", filepath.Join(root, "folder ", "archive.db"), filepath.Join(root, "folder.", "archive.db")} {
		if SQLiteModifiedAfter(alias, cutoff) {
			t.Errorf("ambiguous path selected the canonical database: %q", alias)
		}
	}
}
