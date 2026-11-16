package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	records := []scheduler.RunRecord{
		{Job: "gitcrawl", Repo: "openclaw/old", Status: "success", FinishedAt: "2026-05-22T10:00:00Z"},
		{Job: "gitcrawl", Repo: "openclaw/new", Status: "failed", FinishedAt: "2026-05-22T11:00:00Z"},
	}
	record, ok := latestRecordForJob(records, "gitcrawl")
	if !ok {
		t.Fatal("expected record")
	}
	if record.Repo != "openclaw/new" || record.Status != "failed" {
		t.Fatalf("record = %#v", record)
	}
}

func TestLatestRecordForJobUsesLastEntryAtSameTime(t *testing.T) {
	history := []scheduler.RunRecord{
		{Job: "sync", Status: "failed", FinishedAt: "2026-05-22T10:00:00Z"},
		{Job: "sync", Status: "success", FinishedAt: "2026-05-22T10:00:00Z"},
	}
	record, ok := latestRecordForJob(history, "sync")
	if !ok || record.Status != "success" {
		t.Fatalf("latest record = %+v, found = %v; want the later successful run", record, ok)
	}
}

func TestStatusDoesNotUseAnotherJobsHistory(t *testing.T) {
	for _, tc := range []struct {
		name, job, recordedJob, repo string
	}{
		{"job prefix", "sync", "sync:other", ""},
		{"repository key collision", "sync:owner/repo", "sync", "owner/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "crawlctl.toml")
			cfg := scheduler.DefaultConfig()
			cfg.Jobs[tc.job] = scheduler.Job{Enabled: true, Command: []string{"unused"}}
			paths, err := scheduler.Save(configPath, cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(paths.History), 0o700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(scheduler.RunRecord{
				Job: tc.recordedJob, Repo: tc.repo, Status: "success", FinishedAt: "2026-05-22T11:00:00Z",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.History, append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: ioDiscard{}, config: configPath}
			if err := a.runStatus(nil); err != nil {
				t.Fatal(err)
			}
			if want := tc.job + ": never\n"; stdout.String() != want {
				t.Fatalf("status = %q, want %q", stdout.String(), want)
			}
		})
	}
}

func TestStatusKeepsJobsWithCollidingHistoryKeys(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(fmt.Sprint(reversed), func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
			cfg := scheduler.DefaultConfig()
			for _, name := range []string{"sync", "sync:owner/repo"} {
				cfg.Jobs[name] = scheduler.Job{Enabled: true, Command: []string{"unused"}}
			}
			paths, err := scheduler.Save(configPath, cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			records := []scheduler.RunRecord{
				{Job: "sync:owner/repo", Status: "failed", FinishedAt: "2026-05-22T10:00:00Z"},
				{Job: "sync", Repo: "owner/repo", Status: "success", FinishedAt: "2026-05-22T11:00:00Z"},
			}
			if reversed {
				records[0], records[1] = records[1], records[0]
			}
			var history bytes.Buffer
			for _, record := range records {
				if err := json.NewEncoder(&history).Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(paths.History), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.History, history.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: ioDiscard{}, config: configPath}
			if err := a.runStatus(nil); err != nil {
				t.Fatal(err)
			}
			want := "sync: success 2026-05-22T11:00:00Z\nsync:owner/repo: failed 2026-05-22T10:00:00Z\n"
			if stdout.String() != want {
				t.Fatalf("status = %q, want %q", stdout.String(), want)
			}
		})
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
	for _, value := range []string{"-1", "0", "", "bad", "2junk", "1.5", "1e3", "2 3", " 2", "2\n"} {
		for _, joined := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/joined=%t", value, joined), func(t *testing.T) {
				args := []string{"--tail", value}
				if joined {
					args = []string{"--tail=" + value}
				}
				var stdout bytes.Buffer
				a := app{stdout: &stdout, stderr: ioDiscard{}, config: filepath.Join(t.TempDir(), "missing.toml")}
				err := a.runLogs(args)
				var usage usageError
				if !errors.As(err, &usage) || exitCode(err) != 2 || stdout.Len() != 0 {
					t.Fatalf("err = %v, stdout = %q; want usage error without output", err, stdout.String())
				}
			})
		}
	}
}

func TestRunLogsHonorsTailCount(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
	paths, err := scheduler.Save(configPath, scheduler.DefaultConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(paths.BaseDir, "job.log")
	if err := os.WriteFile(logPath, []byte("first\nsecond\nthird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := json.Marshal(scheduler.RunRecord{Job: "sync", LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.History, append(history, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--tail", "2"}, {"--tail=2"}, {"--tail=+2"}, {"--tail=02"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: ioDiscard{}, config: configPath}
			if err := a.runLogs(args); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != "second\nthird\n" {
				t.Fatalf("tail = %q", stdout.String())
			}
		})
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
