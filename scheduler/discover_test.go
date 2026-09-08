package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/openclaw/crawlkit/control"
)

func TestDefaultBinariesCoversFamily(t *testing.T) {
	want := []string{"gitcrawl", "discrawl", "notcrawl", "wacrawl", "telecrawl", "slacrawl", "graincrawl", "imsgcrawl", "photoscrawl", "weicrawl"}
	if got := DefaultBinaries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default binaries = %v, want %v", got, want)
	}
	first := DefaultBinaries()
	first[0] = "changed"
	if got := DefaultBinaries()[0]; got != want[0] {
		t.Fatalf("caller mutated defaults: %q", got)
	}
}

func TestDiscoverUsesOnlySelectedMetadataAndSupportedJobs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX executable fixtures")
	}
	dir := t.TempDir()
	for _, id := range []string{"graincrawl", "imsgcrawl", "photoscrawl", "weicrawl"} {
		manifest := control.NewManifest(id, id, id)
		command := "sync"
		if id == "photoscrawl" {
			command = "import_apple"
		}
		manifest.Commands = map[string]control.Command{
			command: {Argv: []string{id, command, "--json"}, Mutates: true},
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		script := fmt.Sprintf("#!/bin/sh\n[ \"$#\" = 2 ] && [ \"$1\" = metadata ] && [ \"$2\" = --json ] || exit 9\nprintf '%%s\\n' '%s'\n", data)
		if err := os.WriteFile(filepath.Join(dir, id), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	apps := Discover(context.Background(), nil)
	found := 0
	for _, app := range apps {
		if !app.Found {
			continue
		}
		found++
		if app.Manifest == nil || app.Legacy {
			t.Fatalf("metadata not used: %+v", app)
		}
		job, ok := DefaultJobForApp(app, nil)
		if app.ID == "photoscrawl" {
			if ok {
				t.Fatalf("discovery enabled automatic photo ingestion: %+v", job)
			}
		} else if !ok || !reflect.DeepEqual(job.Command, []string{app.Path, "sync", "--json"}) {
			t.Fatalf("manifest sync not retained: app=%+v job=%+v", app, job)
		}
	}
	if found != 4 {
		t.Fatalf("found = %d, want 4", found)
	}
	explicit := Discover(context.Background(), []string{"graincrawl", "graincrawl", " "})
	if len(explicit) != 1 || explicit[0].ID != "graincrawl" {
		t.Fatalf("explicit override/deduplication changed: %+v", explicit)
	}
	if err := os.WriteFile(filepath.Join(dir, "weicrawl"), []byte("#!/bin/sh\nprintf '{invalid}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := Discover(context.Background(), []string{"weicrawl"})[0]
	if invalid.Manifest != nil || invalid.Legacy {
		t.Fatalf("invalid metadata received invented manifest: %+v", invalid)
	}
}
