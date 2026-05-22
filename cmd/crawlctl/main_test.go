package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openclaw/crawlkit/scheduler"
)

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
	if !strings.Contains(stdout.String(), "OnUnitActiveSec=7min") {
		t.Fatalf("install output = %s", stdout.String())
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
