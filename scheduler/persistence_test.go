package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestHistoryRoundTripsLongCommandRecords(t *testing.T) {
	for _, newline := range []bool{false, true} {
		t.Run(map[bool]string{false: "without newline", true: "with newline"}[newline], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runs.jsonl")
			first := RunRecord{ID: "first", Job: "fixture", Command: []string{"echo", strings.Repeat("x", 70000)}, Status: "success"}
			data, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			if newline {
				data = append(data, '\n')
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadHistory(path)
			if err != nil || !reflect.DeepEqual(got, []RunRecord{first}) {
				t.Fatalf("read long record: count=%d err=%v", len(got), err)
			}
			second := RunRecord{ID: "second", Job: "fixture", Status: "success"}
			if err := appendHistory(path, second); err != nil {
				t.Fatal(err)
			}
			got, err = ReadHistory(path)
			if err != nil || !reflect.DeepEqual(got, []RunRecord{first, second}) {
				t.Fatalf("append after long record: count=%d err=%v", len(got), err)
			}
		})
	}
}

func TestHistoryRepairsLongInterruptedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	first := RunRecord{ID: "first", Job: "fixture", Status: "success"}
	if err := appendHistory(path, first); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString(`{"command":["` + strings.Repeat("x", 70000))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write tail: %v %v", writeErr, closeErr)
	}
	got, err := ReadHistory(path)
	if err != nil || !reflect.DeepEqual(got, []RunRecord{first}) {
		t.Fatalf("read interrupted tail: count=%d err=%v", len(got), err)
	}
	second := RunRecord{ID: "second", Job: "fixture", Status: "success"}
	if err := appendHistory(path, second); err != nil {
		t.Fatal(err)
	}
	got, err = ReadHistory(path)
	if err != nil || !reflect.DeepEqual(got, []RunRecord{first, second}) {
		t.Fatalf("repair interrupted tail: count=%d err=%v", len(got), err)
	}
}

func TestSaveKeepsExistingConfigPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Runner.Every = "5m"
	if _, err := Save(path, cfg, true); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil || loaded.Runner.Every != "5m" {
		t.Fatalf("load saved config: %#v, %v", loaded, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("saved config mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestExplicitConfigPathsDoNotRequireHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	paths, err := DefaultPaths(path)
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != path || paths.LogDir != filepath.Join(filepath.Dir(path), "logs") || paths.StateDir != filepath.Join(filepath.Dir(path), "state") {
		t.Fatalf("explicit paths = %#v", paths)
	}
	for _, homePath := range []string{"", "~/config.toml"} {
		if _, err := DefaultPaths(homePath); err == nil {
			t.Fatalf("%q should still require a home", homePath)
		}
	}
}
