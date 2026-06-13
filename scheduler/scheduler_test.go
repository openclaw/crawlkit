package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExpandRepoJob(t *testing.T) {
	job := Job{Command: []string{"gitcrawl", "refresh", "{repo}", "--json"}, Repos: []string{"a/b", "c/d"}}
	expanded, err := expandJob("gitcrawl", job)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(expanded) != 2 || expanded[1].command[2] != "c/d" {
		t.Fatalf("expanded = %#v", expanded)
	}
}

func TestDefaultJobForAppUsesDiscoveredPath(t *testing.T) {
	job, ok := DefaultJobForApp(App{ID: "gitcrawl", Binary: "gitcrawl", Path: "/opt/homebrew/bin/gitcrawl"}, []string{"openclaw/openclaw"})
	if !ok {
		t.Fatal("expected job")
	}
	if job.Command[0] != "/opt/homebrew/bin/gitcrawl" {
		t.Fatalf("command = %#v", job.Command)
	}
}

func TestPlanInstallBackends(t *testing.T) {
	paths := Paths{ConfigPath: "/tmp/crawlctl.toml", LogDir: "/tmp/logs"}
	for _, backend := range []string{"launchd", "systemd", "windows", "cron"} {
		plan, err := PlanInstall(InstallOptions{Backend: backend, Every: "5m", Executable: "/bin/crawlctl", Paths: paths})
		if err != nil {
			t.Fatalf("%s plan: %v", backend, err)
		}
		if plan.Backend == "" {
			t.Fatalf("%s missing backend", backend)
		}
	}
}

func TestPlanInstallRejectsInexactMinuteBackends(t *testing.T) {
	paths := Paths{ConfigPath: "/tmp/crawlctl.toml", LogDir: "/tmp/logs"}
	plan, err := PlanInstall(InstallOptions{Backend: "systemd", Every: "90s", Executable: "/bin/crawlctl", Paths: paths})
	if err != nil {
		t.Fatalf("systemd plan: %v", err)
	}
	if !strings.Contains(plan.Content, "OnUnitActiveSec=90s") {
		t.Fatalf("systemd content = %s", plan.Content)
	}
	for _, backend := range []string{"windows", "cron"} {
		if _, err := PlanInstall(InstallOptions{Backend: backend, Every: "90s", Executable: "/bin/crawlctl", Paths: paths}); err == nil {
			t.Fatalf("expected %s to reject 90s", backend)
		}
	}
	if _, err := PlanInstall(InstallOptions{Backend: "cron", Every: "90m", Executable: "/bin/crawlctl", Paths: paths}); err == nil {
		t.Fatal("expected cron to reject 90m")
	}
}

func TestPlanInstallLaunchdEscapesXML(t *testing.T) {
	paths := Paths{ConfigPath: "/tmp/a&b/crawlctl.toml", LogDir: "/tmp/logs<private>"}
	plan, err := PlanInstall(InstallOptions{Backend: "launchd", Every: "5m", Executable: "/bin/crawlctl", Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Content, "/tmp/a&amp;b/crawlctl.toml") || !strings.Contains(plan.Content, "logs&lt;private&gt;") {
		t.Fatalf("content was not escaped:\n%s", plan.Content)
	}
}

func TestRunRecordsHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: filepath.Join(dir, "state", "runs.jsonl")}
	cfg := DefaultConfig()
	cfg.Jobs["ok"] = Job{Enabled: true, Command: []string{"sh", "-c", "echo ok"}}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(records) != 1 || records[0].Status != "success" {
		t.Fatalf("records = %#v", records)
	}
	if _, err := os.Stat(records[0].LogPath); err != nil {
		t.Fatalf("log: %v", err)
	}
	history, err := ReadHistory(paths.History)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d", len(history))
	}
}

func TestRunRecoversInvalidStaleLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: filepath.Join(dir, "state", "runs.jsonl")}
	if err := os.MkdirAll(paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LockPath, []byte("pid=0\nstarted_at=2026-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Jobs["ok"] = Job{Enabled: true, Command: []string{"sh", "-c", "echo ok"}}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(records) != 1 || records[0].Status != "success" {
		t.Fatalf("records = %#v", records)
	}
}

func TestRunReturnsHistoryAppendError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	historyDir := filepath.Join(dir, "history-is-dir")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: historyDir}
	cfg := DefaultConfig()
	cfg.Jobs["ok"] = Job{Enabled: true, Command: []string{"sh", "-c", "echo ok"}}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths})
	if err == nil {
		t.Fatal("expected history append error")
	}
	if len(records) != 1 || records[0].Status != "success" {
		t.Fatalf("records = %#v", records)
	}
}

func TestRunCapsLogBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: filepath.Join(dir, "state", "runs.jsonl")}
	cfg := DefaultConfig()
	cfg.Runner.MaxLogBytes = 3
	cfg.Jobs["ok"] = Job{Enabled: true, Command: []string{"sh", "-c", "printf 12345"}}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(records[0].LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "123" {
		t.Fatalf("log = %q", data)
	}
}

func TestRunSkipsJobsThatAreNotDue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: filepath.Join(dir, "state", "runs.jsonl")}
	cfg := DefaultConfig()
	cfg.Jobs["ok"] = Job{Enabled: true, Every: "10m", Command: []string{"sh", "-c", "echo ok"}}
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	if _, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths, Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths, Now: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %#v, want skipped", records)
	}
}

func TestParseEveryRejectsSubMinute(t *testing.T) {
	if _, err := ParseEvery("30s"); err == nil {
		t.Fatal("expected reject error")
	}
}

func TestDefaultPathsCustomConfigKeepsStateNearby(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crawlctl.toml")
	paths, err := DefaultPaths(path)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if filepath.Dir(paths.History) != filepath.Join(filepath.Dir(path), "state") {
		t.Fatalf("history = %s, want state next to config", paths.History)
	}
}
