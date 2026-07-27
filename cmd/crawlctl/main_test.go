package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/scheduler"
)

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name   string
		linked string
		info   *debug.BuildInfo
		ok     bool
		want   string
	}{
		{name: "linked release", linked: "0.14.4", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.14.5"}}, ok: true, want: "0.14.4"},
		{name: "module release", linked: "dev", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.14.5"}}, ok: true, want: "0.14.5"},
		{name: "development build", linked: "dev", info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, ok: true, want: "dev"},
		{name: "missing build info", linked: "dev", ok: false, want: "dev"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveVersion(test.linked, test.info, test.ok); got != test.want {
				t.Fatalf("resolveVersion() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLatestRecordForJobAggregatesRepoRuns(t *testing.T) {
	records := map[string]scheduler.RunRecord{
		"gitcrawl:openclaw/old": {Job: "gitcrawl", Repo: "openclaw/old", Status: "success", FinishedAt: "2026-05-22T10:00:00Z"},
		"gitcrawl:openclaw/new": {Job: "gitcrawl", Repo: "openclaw/new", Status: "failed", FinishedAt: "2026-05-22T11:00:00Z"},
	}
	record, ok := latestRecordForJob(records, "gitcrawl")
	if !ok {
		t.Fatal("expected record")
	}
	if record.Repo != "openclaw/new" || record.Status != "failed" {
		t.Fatalf("record = %#v", record)
	}
}

func TestRunJSONDoesNotIncludeProgress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX echo")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "crawlctl.toml")
	if err := os.WriteFile(configPath, []byte(`version = 1
[runner]
every = "1m"
[jobs.echo]
enabled = true
every = "10m"
command = ["/bin/echo", "ok"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := app{stdout: &stdout, stderr: &stderr, json: true, config: configPath}
	if err := a.runJobs(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var records []scheduler.RunRecord
	if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
		t.Fatalf("stdout is not JSON: %q: %v", stdout.String(), err)
	}
	if len(records) != 1 || records[0].Status != "success" {
		t.Fatalf("records = %#v", records)
	}
}

func TestInstallUsesConfiguredRunnerInterval(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "crawlctl.toml")
	if err := os.WriteFile(configPath, []byte(`version = 1
[runner]
every = "7m"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	a := app{stdout: &stdout, stderr: ioDiscard{}, config: configPath}
	if err := a.runInstall([]string{"--backend", "systemd", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "OnUnitActiveSec=420s") {
		t.Fatalf("install output = %s", stdout.String())
	}
}

func TestRunLogsRejectsInvalidTail(t *testing.T) {
	var stdout bytes.Buffer
	a := app{stdout: &stdout, stderr: ioDiscard{}}
	err := a.runLogs([]string{"--tail=-1"})
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want usageError", err)
	}
}

func TestPrintTailHandlesLongLines(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "crawlctl.log")
	longLine := strings.Repeat("x", 128*1024)
	if err := os.WriteFile(logPath, []byte("first\n"+longLine+"\nlast\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := printTail(&stdout, logPath, 2); err != nil {
		t.Fatal(err)
	}
	want := longLine + "\nlast\n"
	if stdout.String() != want {
		t.Fatalf("stdout len = %d, want %d", len(stdout.String()), len(want))
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
