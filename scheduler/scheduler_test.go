package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestPlanInstallRejectsConflictingConfigPaths(t *testing.T) {
	paths := Paths{ConfigPath: "/tmp/crawlctl.toml", LogDir: "/tmp/logs"}
	_, err := PlanInstall(InstallOptions{ConfigPath: "/tmp/other-crawlctl.toml", Backend: "cron", Every: "5m", Executable: "/bin/crawlctl", Paths: paths})
	if err == nil || !strings.Contains(err.Error(), "conflicting config paths") {
		t.Fatalf("err = %v, want conflicting config paths", err)
	}
}

func TestPlanInstallUsesConfigPathWithoutPaths(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "crawlctl.toml")
	plan, err := PlanInstall(InstallOptions{ConfigPath: configPath, Backend: "cron", Every: "5m", Executable: "/bin/crawlctl"})
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if !strings.Contains(plan.Content, "'--config' '"+configPath+"' 'run'") {
		t.Fatalf("plan content = %s", plan.Content)
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
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(stateDir, "runs.jsonl")
	if err := os.WriteFile(historyPath, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(historyPath, 0o600) }()
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: stateDir, LockPath: filepath.Join(stateDir, "lock"), History: historyPath}
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

func TestRunReturnsHistoryReadErrorBeforeRunningJobs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: filepath.Join(dir, "state", "runs.jsonl")}
	if err := os.MkdirAll(paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.History, []byte("{bad json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Jobs["ok"] = Job{Enabled: true, Command: []string{"sh", "-c", "touch " + marker}}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths})
	if err == nil {
		t.Fatal("expected history read error")
	}
	if len(records) != 0 {
		t.Fatalf("records = %#v, want none", records)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("job marker stat = %v, want not exist", statErr)
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

func TestAppendHistoryWritesCompleteJSONLLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	record := RunRecord{
		ID:         "rec1",
		Job:        "ok",
		Command:    []string{"echo", "ok"},
		Status:     "success",
		StartedAt:  "2026-08-29T00:00:00Z",
		FinishedAt: "2026-08-29T00:00:01Z",
		DurationMs: 1000,
		LogPath:    "/tmp/ok.log",
	}
	if err := appendHistory(path, record); err != nil {
		t.Fatalf("appendHistory: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("history = %q, want one newline-terminated JSONL line", data)
	}
	if bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatalf("history = %q, want exactly one line", data)
	}
	history, err := ReadHistory(path)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history) != 1 || history[0].ID != record.ID || history[0].Job != record.Job {
		t.Fatalf("history = %#v", history)
	}
}

type historyCloseWriter struct {
	writes   [][]byte
	closeErr error
}

func (w *historyCloseWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (w *historyCloseWriter) Close() error {
	return w.closeErr
}

func TestWriteHistoryRecordWritesCompleteLine(t *testing.T) {
	w := &historyCloseWriter{}
	record := RunRecord{ID: "rec1", Job: "ok", Status: "success"}
	if err := writeHistoryRecord(w, record); err != nil {
		t.Fatalf("writeHistoryRecord: %v", err)
	}
	if len(w.writes) != 1 {
		t.Fatalf("writes = %d, want 1 complete line", len(w.writes))
	}
	line := w.writes[0]
	if len(line) == 0 || line[len(line)-1] != '\n' {
		t.Fatalf("write = %q, want newline-terminated JSONL", line)
	}
	var got RunRecord
	if err := json.Unmarshal(bytes.TrimSpace(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != record.ID || got.Job != record.Job {
		t.Fatalf("got %#v", got)
	}
}

func TestWriteHistoryRecordReturnsCloseError(t *testing.T) {
	w := &historyCloseWriter{closeErr: errors.New("flush failed")}
	err := writeHistoryRecord(w, RunRecord{ID: "rec1", Job: "ok", Status: "success"})
	if err == nil {
		t.Fatal("expected close error")
	}
	if !strings.Contains(err.Error(), "flush failed") {
		t.Fatalf("err = %v, want close error", err)
	}
}
