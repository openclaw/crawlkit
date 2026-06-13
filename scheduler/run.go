package scheduler

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type RunOptions struct {
	Config Config
	Paths  Paths
	Names  []string
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

type RunRecord struct {
	ID         string   `json:"id"`
	Job        string   `json:"job"`
	Command    []string `json:"command"`
	Repo       string   `json:"repo,omitempty"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at"`
	DurationMs int64    `json:"duration_ms"`
	ExitCode   int      `json:"exit_code"`
	Status     string   `json:"status"`
	LogPath    string   `json:"log_path"`
	Error      string   `json:"error,omitempty"`
}

func Run(ctx context.Context, opts RunOptions) ([]RunRecord, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if err := ensureRuntimeDirs(opts.Paths); err != nil {
		return nil, err
	}
	release, err := acquireLock(opts.Paths.LockPath)
	if err != nil {
		return nil, err
	}
	defer release()
	history, _ := ReadHistory(opts.Paths.History)
	last := LastRecords(history)

	names := selectedNames(opts.Config, opts.Names)
	records := []RunRecord{}
	var historyErr error
	for _, name := range names {
		job := opts.Config.Jobs[name]
		if !job.Enabled && len(opts.Names) == 0 {
			continue
		}
		expanded, err := expandJob(name, job)
		if err != nil {
			record := failedRecord(now, name, nil, "", opts.Paths.LogDir, err)
			records = append(records, record)
			if err := appendHistory(opts.Paths.History, record); err != nil && historyErr == nil {
				historyErr = err
			}
			continue
		}
		for _, item := range expanded {
			lastRun, ok := lastRecord(last, name, item.repo)
			if len(opts.Names) == 0 && !jobDue(now(), job, lastRun, ok) {
				continue
			}
			record := runCommand(ctx, now, opts.Paths.LogDir, name, item.repo, item.command, job.WorkDir, job.Env, opts.Config.Runner.MaxLogBytes)
			records = append(records, record)
			if err := appendHistory(opts.Paths.History, record); err != nil && historyErr == nil {
				historyErr = err
			}
			if record.Status == "success" {
				fmt.Fprintf(opts.Stdout, "%s ok duration=%dms\n", record.Job, record.DurationMs)
			} else {
				fmt.Fprintf(opts.Stderr, "%s failed exit=%d log=%s\n", record.Job, record.ExitCode, record.LogPath)
			}
		}
	}
	return records, historyErr
}

func lastRecord(records map[string]RunRecord, name, repo string) (RunRecord, bool) {
	if repo != "" {
		record, ok := records[name+":"+repo]
		return record, ok
	}
	record, ok := records[name]
	return record, ok
}

func jobDue(now time.Time, job Job, last RunRecord, ok bool) bool {
	if !ok || last.Status != "success" {
		return true
	}
	every, err := ParseEvery(job.Every)
	if err != nil {
		return true
	}
	finished, err := time.Parse(time.RFC3339, last.FinishedAt)
	if err != nil {
		return true
	}
	return now.Sub(finished) >= every
}

type expandedJob struct {
	repo    string
	command []string
}

func selectedNames(cfg Config, requested []string) []string {
	if len(requested) > 0 {
		return append([]string(nil), requested...)
	}
	names := make([]string, 0, len(cfg.Jobs))
	for name := range cfg.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func expandJob(name string, job Job) ([]expandedJob, error) {
	if len(job.Command) == 0 {
		return nil, fmt.Errorf("%s has no command", name)
	}
	hasRepo := false
	for _, arg := range job.Command {
		if strings.Contains(arg, "{repo}") {
			hasRepo = true
			break
		}
	}
	if !hasRepo {
		return []expandedJob{{command: append([]string(nil), job.Command...)}}, nil
	}
	if len(job.Repos) == 0 {
		return nil, fmt.Errorf("%s uses {repo} but has no repos", name)
	}
	out := make([]expandedJob, 0, len(job.Repos))
	for _, repo := range job.Repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		command := make([]string, len(job.Command))
		for i, arg := range job.Command {
			command[i] = strings.ReplaceAll(arg, "{repo}", repo)
		}
		out = append(out, expandedJob{repo: repo, command: command})
	}
	return out, nil
}

func runCommand(ctx context.Context, now func() time.Time, logDir, jobName, repo string, command []string, workDir string, env []string, maxLogBytes int64) RunRecord {
	start := now().UTC()
	record := RunRecord{
		ID:        randomID(),
		Job:       jobName,
		Command:   append([]string(nil), command...),
		Repo:      repo,
		StartedAt: start.Format(time.RFC3339),
		Status:    "failed",
		ExitCode:  1,
	}
	if len(command) == 0 {
		record.Error = "empty command"
		return finishRecord(now, record, start)
	}
	logPath := filepath.Join(logDir, safeName(jobName)+"-"+start.Format("20060102T150405Z")+"-"+record.ID+".log")
	record.LogPath = logPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		record.Error = err.Error()
		return finishRecord(now, record, start)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		record.Error = err.Error()
		return finishRecord(now, record, start)
	}
	defer logFile.Close()
	logWriter := io.Writer(logFile)
	if maxLogBytes > 0 {
		logWriter = &limitedLogWriter{w: logFile, max: maxLogBytes}
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if strings.TrimSpace(workDir) != "" {
		cmd.Dir = workDir
	}
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Run(); err != nil {
		record.Error = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			record.ExitCode = exitErr.ExitCode()
		}
		return finishRecord(now, record, start)
	}
	record.Status = "success"
	record.ExitCode = 0
	return finishRecord(now, record, start)
}

type limitedLogWriter struct {
	w       io.Writer
	max     int64
	written int64
}

func (w *limitedLogWriter) Write(p []byte) (int, error) {
	if w.max <= 0 || w.written >= w.max {
		return len(p), nil
	}
	remaining := w.max - w.written
	toWrite := p
	if int64(len(toWrite)) > remaining {
		toWrite = toWrite[:remaining]
	}
	n, err := w.w.Write(toWrite)
	w.written += int64(n)
	if err != nil {
		return n, err
	}
	return len(p), nil
}

func finishRecord(now func() time.Time, record RunRecord, start time.Time) RunRecord {
	finished := now().UTC()
	record.FinishedAt = finished.Format(time.RFC3339)
	record.DurationMs = finished.Sub(start).Milliseconds()
	return record
}

func failedRecord(now func() time.Time, name string, command []string, repo, logDir string, err error) RunRecord {
	start := now().UTC()
	record := RunRecord{
		ID:         randomID(),
		Job:        name,
		Command:    append([]string(nil), command...),
		Repo:       repo,
		StartedAt:  start.Format(time.RFC3339),
		FinishedAt: start.Format(time.RFC3339),
		Status:     "failed",
		ExitCode:   1,
		Error:      err.Error(),
		LogPath:    filepath.Join(logDir, safeName(name)+"-"+start.Format("20060102T150405Z")+".log"),
	}
	return record
}

func appendHistory(path string, record RunRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	return enc.Encode(record)
}

func ReadHistory(path string) ([]RunRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var records []RunRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record RunRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err == nil {
			records = append(records, record)
		}
	}
	return records, scanner.Err()
}

func LastRecords(records []RunRecord) map[string]RunRecord {
	out := map[string]RunRecord{}
	for _, record := range records {
		key := record.Job
		if record.Repo != "" {
			key += ":" + record.Repo
		}
		out[key] = record
	}
	return out
}

func ensureRuntimeDirs(paths Paths) error {
	for _, path := range []string{paths.LogDir, paths.StateDir} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func acquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if stale, staleErr := lockIsStale(path); staleErr == nil && stale {
				if removeErr := os.Remove(path); removeErr != nil {
					return nil, fmt.Errorf("remove stale crawlctl lock %s: %w", path, removeErr)
				}
				return acquireLock(path)
			}
			return nil, fmt.Errorf("crawlctl already running: %s", path)
		}
		return nil, err
	}
	_, _ = fmt.Fprintf(file, "pid=%d\nstarted_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	_ = file.Close()
	return func() { _ = os.Remove(path) }, nil
}

func lockIsStale(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "pid" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || pid <= 0 {
			return true, nil
		}
		return !processExists(pid), nil
	}
	return true, nil
}

func processExists(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || !errors.Is(err, os.ErrProcessDone)
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "job"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func randomID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
