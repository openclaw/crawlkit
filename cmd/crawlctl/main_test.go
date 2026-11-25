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
	"strconv"
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

func TestLatestRecordsForJobsAggregatesRepoRuns(t *testing.T) {
	records := []scheduler.RunRecord{
		{Job: "gitcrawl", Repo: "openclaw/old", Status: "success", FinishedAt: "2026-05-22T10:00:00Z"},
		{Job: "gitcrawl", Repo: "openclaw/new", Status: "failed", FinishedAt: "2026-05-22T11:00:00Z"},
		{Job: "gitcrawl", Repo: "openclaw/older", Status: "success", FinishedAt: "2026-05-22T09:00:00Z"},
	}
	record, ok := latestRecordsForJobs(records)["gitcrawl"]
	if !ok {
		t.Fatal("expected record")
	}
	if record.Repo != "openclaw/new" || record.Status != "failed" {
		t.Fatalf("record = %#v", record)
	}
}

func TestLatestRecordsForJobsUsesLastEntryAtSameTime(t *testing.T) {
	history := []scheduler.RunRecord{
		{Job: "sync", Status: "failed", FinishedAt: "2026-05-22T10:00:00Z"},
		{Job: "sync", Status: "success", FinishedAt: "2026-05-22T10:00:00Z"},
	}
	record, ok := latestRecordsForJobs(history)["sync"]
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

func TestRunLogsReportsOutputFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
	paths, err := scheduler.Save(configPath, scheduler.DefaultConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(paths.BaseDir, "job.log")
	const data = "first\nsecond\nthird\n"
	if err := os.WriteFile(logPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := json.Marshal(scheduler.RunRecord{Job: "sync", LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.History, append(history, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, capacity := range []int{0, 8} {
		t.Run(fmt.Sprintf("after_%d_bytes", capacity), func(t *testing.T) {
			outputErr := errors.New("output unavailable")
			var written bytes.Buffer
			failed := false
			writer := logWriterFunc(func(p []byte) (int, error) {
				if failed {
					t.Error("attempted another write after output failed")
				}
				n := min(len(p), capacity-written.Len())
				written.Write(p[:n])
				if n < len(p) {
					failed = true
					return n, outputErr
				}
				return n, nil
			})
			a := app{stdout: writer, stderr: ioDiscard{}}
			err := a.run(t.Context(), []string{"--config", configPath, "logs", "--tail", "3", "sync"})
			if !errors.Is(err, outputErr) || exitCode(err) != 1 {
				t.Fatalf("logs error = %v, exit = %d; want output failure and exit 1", err, exitCode(err))
			}
			if written.String() != data[:capacity] {
				t.Fatalf("output = %q, want %q", written.String(), data[:capacity])
			}
		})
	}
}

type logWriterFunc func([]byte) (int, error)

func (f logWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestRunJobsRejectsBlankRepositories(t *testing.T) {
	for _, tc := range []struct {
		name  string
		repos []string
	}{
		{"missing", nil},
		{"empty", []string{""}},
		{"whitespace", []string{" \t\r\n"}},
		{"multiple blanks", []string{"", " ", "\t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
			cfg := scheduler.DefaultConfig()
			cfg.Jobs["refresh"] = scheduler.Job{Enabled: true, Command: []string{"unused", "{repo}"}, Repos: tc.repos}
			paths, err := scheduler.Save(configPath, cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: ioDiscard{}, json: true, config: configPath}
			err = a.runJobs(t.Context(), nil)
			var failure jobFailureError
			if !errors.As(err, &failure) || failure.Count != 1 || exitCode(err) != 1 {
				t.Fatalf("run error = %v; want one failed job", err)
			}
			var records []scheduler.RunRecord
			if err := json.Unmarshal(stdout.Bytes(), &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Job != "refresh" || records[0].Status != "failed" || !strings.Contains(records[0].Error, "has no repos") {
				t.Fatalf("run records = %+v", records)
			}
			history, err := scheduler.ReadHistory(paths.History)
			if err != nil || len(history) != 1 || history[0].ID != records[0].ID {
				t.Fatalf("failed job not recorded in history: %+v, %v", history, err)
			}
		})
	}
}

func TestRunHonorsFlagSeparatorForJobNames(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ordinary", "--version", "--json", "--config=literal"} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", name, jsonOutput), func(t *testing.T) {
				configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
				jobName := name
				if strings.HasPrefix(name, "--config=") {
					jobName = "--config=" + filepath.Join(t.TempDir(), "missing.toml")
				}
				cfg := scheduler.DefaultConfig()
				for _, job := range []string{jobName, "other"} {
					cfg.Jobs[job] = scheduler.Job{Enabled: true, Command: []string{exe, "-test.run=^$"}}
				}
				paths, err := scheduler.Save(configPath, cfg, false)
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"--config", configPath, "run"}
				if jsonOutput {
					args = append(args, "--json")
				}
				args = append(args, "--", jobName)
				var stdout bytes.Buffer
				a := app{stdout: &stdout, stderr: ioDiscard{}}
				if err := a.run(t.Context(), args); err != nil {
					t.Fatal(err)
				}
				history, err := scheduler.ReadHistory(paths.History)
				if err != nil || len(history) != 1 || history[0].Job != jobName || history[0].Status != "success" {
					t.Fatalf("selected job not run exclusively: %+v, %v", history, err)
				}
				if a.json != jsonOutput {
					t.Fatalf("JSON mode = %t, want %t", a.json, jsonOutput)
				}
				if jsonOutput {
					var records []scheduler.RunRecord
					if err := json.Unmarshal(stdout.Bytes(), &records); err != nil || len(records) != 1 || records[0].Job != jobName {
						t.Fatalf("JSON output = %q, %v", stdout.String(), err)
					}
				}
			})
		}
	}
}

func TestInstallKeepsRelativeConfigAndLogPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	relativeConfig := filepath.Join("config space", "crawlctl.toml")
	absoluteConfig, err := filepath.Abs(relativeConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg := scheduler.DefaultConfig()
	cfg.Runner.Every = "12m"
	paths, err := scheduler.Save(absoluteConfig, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"cron", "launchd", "systemd", "windows"} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/override=%t", backend, override), func(t *testing.T) {
				args := []string{"--backend", backend, "--dry-run"}
				if override {
					args = append(args, "--every", "5m")
				}
				var stdout bytes.Buffer
				a := app{stdout: &stdout, stderr: ioDiscard{}, config: relativeConfig, json: true}
				if err := a.runInstall(args); err != nil {
					t.Fatal(err)
				}
				var plan scheduler.InstallPlan
				if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
					t.Fatal(err)
				}
				content := plan.Content + strings.Join(plan.Command, " ")
				wantConfig := absoluteConfig
				if backend == "systemd" {
					wantConfig = strconv.Quote(wantConfig)
				}
				if !strings.Contains(content, wantConfig) {
					t.Fatalf("scheduled config no longer names the selected file: %s", content)
				}
				if (backend == "cron" || backend == "launchd") && !strings.Contains(content, paths.LogDir) {
					t.Fatalf("scheduled logs are relative to a different working directory: %s", content)
				}
			})
		}
	}
}

func TestInstallPreservesSymlinkParentConfigPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink parent traversal semantics")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, "target", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target", "nested"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	cfg := scheduler.DefaultConfig()
	cfg.Runner.Every = "12m"
	if _, err := scheduler.Save(filepath.Join(dir, "target", "crawlctl.toml"), cfg, false); err != nil {
		t.Fatal(err)
	}
	cfg.Runner.Every = "5m"
	if _, err := scheduler.Save(filepath.Join(dir, "crawlctl.toml"), cfg, false); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	a := app{stdout: &stdout, stderr: ioDiscard{}, config: "link/../crawlctl.toml", json: true}
	if err := a.runInstall([]string{"--backend", "cron", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	var plan scheduler.InstallPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Content, "*/12 ") || !strings.Contains(plan.Content, "'"+cwd+"/link/../crawlctl.toml'") {
		t.Fatalf("schedule selected a different config: %s", plan.Content)
	}
}

func TestLogsSelectsFlagLikeJobNames(t *testing.T) {
	for _, name := range []string{"plain", "--version", "--json", "--config=literal", "--tail=1", "--"} {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
			paths, err := scheduler.Save(configPath, scheduler.DefaultConfig(), false)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			var history bytes.Buffer
			for _, record := range []scheduler.RunRecord{
				{Job: name, LogPath: filepath.Join(paths.BaseDir, "selected.log")},
				{Job: "other", LogPath: filepath.Join(paths.BaseDir, "other.log")},
			} {
				if err := os.WriteFile(record.LogPath, []byte(record.Job+" first\n"+record.Job+" last\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := json.NewEncoder(&history).Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(paths.History, history.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			a := app{stdout: &stdout, stderr: ioDiscard{}}
			args := []string{"--config", configPath, "logs", "--tail", "1", "--", name}
			if err := a.run(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if want := name + " last\n"; stdout.String() != want {
				t.Fatalf("logs = %q, want %q", stdout.String(), want)
			}
			stdout.Reset()
			if err := a.run(t.Context(), []string{"--config", configPath, "logs", "--tail=1", "--"}); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != "other last\n" {
				t.Fatalf("latest logs = %q", stdout.String())
			}
		})
	}
}

func TestLogsRejectsMultipleJobsAcrossSeparator(t *testing.T) {
	for _, args := range [][]string{{"first", "--", "second"}, {"--", "first", "second"}} {
		var stdout bytes.Buffer
		a := app{stdout: &stdout, stderr: ioDiscard{}, config: filepath.Join(t.TempDir(), "missing.toml")}
		err := a.runLogs(args)
		var usage usageError
		if !errors.As(err, &usage) || !strings.Contains(err.Error(), "at most one job") || stdout.Len() != 0 {
			t.Fatalf("logs(%q): err = %v, stdout = %q", args, err, stdout.String())
		}
	}
}
