package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestReadHistoryIgnoresTruncatedLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	complete := `{"id":"1","job":"ok","command":["true"],"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","duration_ms":1,"exit_code":0,"status":"success","log_path":"ok.log"}` + "\n"
	if err := os.WriteFile(path, []byte(complete+`{"id":"2","job":"ok"`), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := ReadHistory(path)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 || history[0].ID != "1" {
		t.Fatalf("history = %#v", history)
	}
}

func TestRunDoesNotRefuseOnTruncatedHistoryLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command path differs on windows")
	}
	dir := t.TempDir()
	paths := Paths{LogDir: filepath.Join(dir, "logs"), StateDir: filepath.Join(dir, "state"), LockPath: filepath.Join(dir, "state", "lock"), History: filepath.Join(dir, "state", "runs.jsonl")}
	if err := os.MkdirAll(paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	complete := `{"id":"1","job":"ok","command":["true"],"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","duration_ms":1,"exit_code":0,"status":"success","log_path":"ok.log"}` + "\n"
	if err := os.WriteFile(paths.History, []byte(complete+`{"id":"2","job":"ok"`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Jobs["ok"] = Job{Enabled: true, Command: []string{"sh", "-c", "echo ok"}}
	records, err := Run(context.Background(), RunOptions{Config: cfg, Paths: paths, Names: []string{"ok"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(records) != 1 || records[0].Status != "success" {
		t.Fatalf("records = %#v", records)
	}
	history, err := ReadHistory(paths.History)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 || history[0].ID != "1" || history[1].ID == "" || history[1].ID == "1" {
		t.Fatalf("history = %#v", history)
	}
	data, err := os.ReadFile(paths.History)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("history file = %q, want complete JSONL", data)
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

func TestHistoryRetainsValidRecordAtEOF(t *testing.T) {
	for _, prefix := range []string{"", "{\"id\":\"first\"}\n"} {
		t.Run(prefix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runs.jsonl")
			original := prefix + `{"id":"last","job":"ok","error":"` + strings.Repeat("x", 5000) + `"}` + "\r"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			history, err := ReadHistory(path)
			if err != nil || len(history) == 0 || history[len(history)-1].ID != "last" {
				t.Fatalf("valid EOF record lost: history=%v err=%v", history, err)
			}
			if err := appendHistory(path, RunRecord{ID: "next"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(data, []byte(original+"\n")) {
				t.Fatalf("append changed existing bytes: %q", data)
			}
			after, err := ReadHistory(path)
			if err != nil || len(after) != len(history)+1 || after[len(after)-1].ID != "next" {
				t.Fatalf("append history=%v err=%v", after, err)
			}
		})
	}
}

func TestHistoryRejectsCorruptFinalRecord(t *testing.T) {
	for _, tail := range []string{`{"id":!}`, `{"duration_ms":"wrong type"}`, `{"id":"one"}{"id":`, "{bad json}\n"} {
		t.Run(tail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runs.jsonl")
			original := []byte("{\"id\":\"keep\"}\n" + tail)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadHistory(path); err == nil {
				t.Fatal("corruption must remain visible")
			}
			if !bytes.HasSuffix(original, []byte{'\n'}) {
				if err := appendHistory(path, RunRecord{ID: "next"}); err == nil {
					t.Fatal("append must reject corrupt tail")
				}
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, original) {
				t.Fatalf("corrupt history changed: %q err=%v", data, err)
			}
		})
	}
}

func TestHistoryRecoversEveryPartialRecordPrefix(t *testing.T) {
	// Every interrupted byte boundary of a normal encoded record is recoverable.
	line, err := json.Marshal(RunRecord{ID: "partial", Job: "café", Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	prefix := []byte("{\"id\":\"keep\"}\n")
	for cut := 1; cut < len(line); cut++ {
		data := append(bytes.Clone(prefix), line[:cut]...)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		history, err := ReadHistory(path)
		if err != nil || len(history) != 1 || history[0].ID != "keep" {
			t.Fatalf("cut %d: history=%v err=%v", cut, history, err)
		}
		if err := appendHistory(path, RunRecord{ID: "next"}); err != nil {
			t.Fatalf("cut %d: append: %v", cut, err)
		}
		history, err = ReadHistory(path)
		if err != nil || len(history) != 2 || history[0].ID != "keep" || history[1].ID != "next" {
			t.Fatalf("cut %d: recovered history=%v err=%v", cut, history, err)
		}
	}
}

type failingHistoryFile struct {
	*os.File
	failWrite   bool
	writeErr    error
	truncateErr error
	closeErr    error
}

func (f failingHistoryFile) Write(p []byte) (int, error) {
	if !f.failWrite {
		return f.File.Write(p)
	}
	n, err := f.File.Write(p[:len(p)/2])
	return n, errors.Join(err, f.writeErr)
}

func (f failingHistoryFile) Truncate(size int64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	return f.File.Truncate(size)
}

func (f failingHistoryFile) Close() error {
	return errors.Join(f.File.Close(), f.closeErr)
}

func TestHistoryWriteFailurePreservesPriorRecords(t *testing.T) {
	writeErr := errors.New("injected disk write failure")
	truncateErr := errors.New("injected truncate failure")
	for _, tc := range []struct {
		name        string
		writeErr    error
		truncateErr error
	}{
		{name: "short write"},
		{name: "write error", writeErr: writeErr},
		{name: "failed rollback", writeErr: writeErr, truncateErr: truncateErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runs.jsonl")
			original := []byte(`{"id":"keep"}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			err = appendHistoryFile(failingHistoryFile{File: file, failWrite: true, writeErr: tc.writeErr, truncateErr: tc.truncateErr}, RunRecord{ID: "failed"})
			wantErr := tc.writeErr
			if wantErr == nil {
				wantErr = io.ErrShortWrite
			}
			if !errors.Is(err, wantErr) || (tc.truncateErr != nil && !errors.Is(err, truncateErr)) {
				t.Fatalf("append error=%v, want write and cleanup failures", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.truncateErr == nil && !bytes.Equal(data, original) {
				t.Fatalf("rollback changed prior bytes: %q", data)
			}
			history, err := ReadHistory(path)
			if err != nil || len(history) != 1 || history[0].ID != "keep" {
				t.Fatalf("partial write hid prior record: %v, %v", history, err)
			}
			if err := appendHistory(path, RunRecord{ID: "next"}); err != nil {
				t.Fatal(err)
			}
			history, err = ReadHistory(path)
			if err != nil || len(history) != 2 || history[0].ID != "keep" || history[1].ID != "next" {
				t.Fatalf("recovery history=%v err=%v", history, err)
			}
		})
	}
}

func TestHistoryReturnsCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("injected close failure")
	err = appendHistoryFile(failingHistoryFile{File: file, closeErr: closeErr}, RunRecord{ID: "complete"})
	if !errors.Is(err, closeErr) {
		t.Fatalf("close error lost: %v", err)
	}
}
