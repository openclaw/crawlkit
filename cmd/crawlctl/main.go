package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openclaw/crawlkit/scheduler"
)

var version = "dev"

const maxLogLineBytes = 10 * 1024 * 1024

type app struct {
	stdout io.Writer
	stderr io.Writer
	json   bool
	config string
}

func main() {
	ctx := context.Background()
	a := app{stdout: os.Stdout, stderr: os.Stderr}
	if err := a.run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCode(err))
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func (a *app) run(ctx context.Context, args []string) error {
	args = hoistGlobalFlags(args)
	fs := flag.NewFlagSet("crawlctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "write JSON output")
	configPath := fs.String("config", "", "config path")
	versionFlag := fs.Bool("version", false, "print version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.printUsage()
			return nil
		}
		return usageError{err}
	}
	a.json = *jsonOut
	a.config = *configPath
	if *versionFlag {
		fmt.Fprintln(a.stdout, version)
		return nil
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		a.printUsage()
		return nil
	}
	switch rest[0] {
	case "init":
		return a.runInit(ctx, rest[1:])
	case "discover":
		return a.runDiscover(ctx, rest[1:])
	case "run":
		return a.runJobs(ctx, rest[1:])
	case "status":
		return a.runStatus(rest[1:])
	case "logs":
		return a.runLogs(rest[1:])
	case "install":
		return a.runInstall(rest[1:])
	case "uninstall":
		return a.runUninstall(rest[1:])
	default:
		return usageError{fmt.Errorf("unknown command %q", rest[0])}
	}
}

func hoistGlobalFlags(args []string) []string {
	out := make([]string, 0, len(args))
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json" || arg == "--version":
			out = append(out, arg)
		case arg == "--config" && i+1 < len(args):
			out = append(out, arg, args[i+1])
			i++
		case strings.HasPrefix(arg, "--config="):
			out = append(out, arg)
		default:
			rest = append(rest, arg)
		}
	}
	return append(out, rest...)
}

func (a *app) printUsage() {
	fmt.Fprint(a.stdout, `crawlctl manages local crawler refresh jobs.

Usage:
  crawlctl [--config PATH] [--json] <command> [args]

Commands:
  init       Discover crawl apps and write a config.
  discover   Print discovered crawl apps.
  run        Run enabled jobs now.
  status     Show last run status.
  logs       Print recent job logs.
  install    Install OS-native periodic schedule.
  uninstall  Remove OS-native periodic schedule.

Examples:
  crawlctl init --repo openclaw/openclaw
  crawlctl run
  crawlctl status
  crawlctl install --dry-run
`)
}

func (a *app) runInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	force := fs.Bool("force", false, "overwrite existing config")
	repos := multiFlag{}
	binaries := multiFlag{}
	fs.Var(&repos, "repo", "gitcrawl repository to refresh")
	fs.Var(&binaries, "app", "app binary to include")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{fmt.Errorf("init takes flags only")}
	}
	apps := scheduler.Discover(ctx, binaries)
	cfg := scheduler.DefaultConfig()
	for _, discovered := range apps {
		job, ok := scheduler.DefaultJobForApp(discovered, repos)
		if !ok || !discovered.Found {
			continue
		}
		cfg.Jobs[discovered.ID] = job
	}
	paths, err := scheduler.Save(a.config, cfg, *force)
	if err != nil {
		return err
	}
	return a.write("init", map[string]any{"config": paths.ConfigPath, "jobs": scheduler.EnabledJobNames(cfg), "discovered": apps})
}

func (a *app) runDiscover(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	binaries := multiFlag{}
	fs.Var(&binaries, "app", "app binary to include")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{fmt.Errorf("discover takes flags only")}
	}
	return a.write("discover", scheduler.Discover(ctx, binaries))
}

func (a *app) runJobs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	cfg, paths, err := scheduler.Load(a.config)
	if err != nil {
		return err
	}
	stdout := a.stdout
	if a.json {
		stdout = io.Discard
	}
	records, err := scheduler.Run(ctx, scheduler.RunOptions{Config: cfg, Paths: paths, Names: fs.Args(), Stdout: stdout, Stderr: a.stderr})
	if err != nil {
		return err
	}
	failed := failedRecords(records)
	if a.json {
		if err := writeJSON(a.stdout, records); err != nil {
			return err
		}
		if failed > 0 {
			return jobFailureError{Count: failed}
		}
		return nil
	}
	for _, record := range records {
		fmt.Fprintf(a.stdout, "%s: %s exit=%d log=%s\n", record.Job, record.Status, record.ExitCode, record.LogPath)
	}
	if failed > 0 {
		return jobFailureError{Count: failed}
	}
	return nil
}

type jobFailureError struct {
	Count int
}

func (e jobFailureError) Error() string {
	if e.Count == 1 {
		return "1 job failed"
	}
	return fmt.Sprintf("%d jobs failed", e.Count)
}

func failedRecords(records []scheduler.RunRecord) int {
	failed := 0
	for _, record := range records {
		if record.Status != "success" {
			failed++
		}
	}
	return failed
}

func (a *app) runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{fmt.Errorf("status takes flags only")}
	}
	cfg, paths, err := scheduler.Load(a.config)
	if err != nil {
		return err
	}
	history, err := scheduler.ReadHistory(paths.History)
	if err != nil {
		return err
	}
	last := scheduler.LastRecords(history)
	if a.json {
		return writeJSON(a.stdout, map[string]any{"config": paths.ConfigPath, "jobs": cfg.Jobs, "last": last})
	}
	names := make([]string, 0, len(cfg.Jobs))
	for name := range cfg.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		job := cfg.Jobs[name]
		state := "disabled"
		detail := ""
		if job.Enabled {
			state = "never"
		}
		if record, ok := latestRecordForJob(last, name); ok {
			state = record.Status
			detail = " " + record.FinishedAt
		}
		fmt.Fprintf(a.stdout, "%s: %s%s\n", name, state, detail)
	}
	return nil
}

func latestRecordForJob(last map[string]scheduler.RunRecord, name string) (scheduler.RunRecord, bool) {
	if record, ok := last[name]; ok {
		return record, true
	}
	var latest scheduler.RunRecord
	found := false
	prefix := name + ":"
	for key, record := range last {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !found || record.FinishedAt > latest.FinishedAt {
			latest = record
			found = true
		}
	}
	return latest, found
}

func (a *app) runLogs(args []string) error {
	tail := 80
	job := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tail":
			if i+1 >= len(args) {
				return usageError{fmt.Errorf("--tail requires a line count")}
			}
			parsed, err := parseTail(args[i+1])
			if err != nil {
				return usageError{err}
			}
			tail = parsed
			i++
		default:
			if strings.HasPrefix(args[i], "--tail=") {
				parsed, err := parseTail(strings.TrimPrefix(args[i], "--tail="))
				if err != nil {
					return usageError{err}
				}
				tail = parsed
				continue
			}
			if strings.HasPrefix(args[i], "-") {
				return usageError{fmt.Errorf("unknown logs flag %q", args[i])}
			}
			if job != "" {
				return usageError{fmt.Errorf("logs accepts at most one job")}
			}
			job = args[i]
		}
	}
	_, paths, err := scheduler.Load(a.config)
	if err != nil {
		return err
	}
	history, err := scheduler.ReadHistory(paths.History)
	if err != nil {
		return err
	}
	for i := len(history) - 1; i >= 0; i-- {
		if job == "" || history[i].Job == job {
			return printTail(a.stdout, history[i].LogPath, tail)
		}
	}
	return fmt.Errorf("no logs found")
}

func parseTail(value string) (int, error) {
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return 0, fmt.Errorf("invalid --tail value %q", value)
	}
	if parsed <= 0 {
		return 0, errors.New("--tail must be greater than zero")
	}
	return parsed, nil
}

func (a *app) runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	backend := fs.String("backend", "auto", "auto|launchd|systemd|windows|cron")
	every := fs.String("every", "", "schedule interval")
	dryRun := fs.Bool("dry-run", false, "print install plan only")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{fmt.Errorf("install takes flags only")}
	}
	paths, err := scheduler.DefaultPaths(a.config)
	if err != nil {
		return err
	}
	installEvery := *every
	if strings.TrimSpace(installEvery) == "" {
		cfg, loadedPaths, err := scheduler.Load(a.config)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			paths = loadedPaths
			installEvery = cfg.Runner.Every
		}
	}
	plan, err := scheduler.Install(scheduler.InstallOptions{Backend: *backend, Every: installEvery, DryRun: *dryRun, Paths: paths})
	if err != nil {
		return err
	}
	return a.write("install", plan)
}

func (a *app) runUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	backend := fs.String("backend", "auto", "auto|launchd|systemd|windows")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{fmt.Errorf("uninstall takes flags only")}
	}
	if err := scheduler.Uninstall(*backend); err != nil {
		return err
	}
	return a.write("uninstall", map[string]any{"backend": *backend, "removed": true})
}

func (a *app) write(label string, value any) error {
	if a.json {
		return writeJSON(a.stdout, value)
	}
	switch v := value.(type) {
	case scheduler.InstallPlan:
		fmt.Fprintf(a.stdout, "backend: %s\n", v.Backend)
		if v.Path != "" {
			fmt.Fprintf(a.stdout, "path: %s\n", v.Path)
		}
		if len(v.Command) > 0 {
			fmt.Fprintf(a.stdout, "command: %s\n", strings.Join(v.Command, " "))
		}
		if v.Content != "" {
			fmt.Fprintln(a.stdout, v.Content)
		}
	case []scheduler.App:
		for _, app := range v {
			state := "missing"
			if app.Found {
				state = "native"
				if app.Legacy {
					state = "legacy"
				}
			}
			fmt.Fprintf(a.stdout, "%s: %s %s\n", app.ID, state, app.Path)
		}
	default:
		fmt.Fprintf(a.stdout, "%s: ok\n", label)
	}
	return nil
}

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func printTail(w io.Writer, path string, limit int) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("missing log path")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLogLineBytes)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > limit {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
