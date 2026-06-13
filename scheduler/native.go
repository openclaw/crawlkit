package scheduler

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"
)

type InstallOptions struct {
	ConfigPath string
	Every      string
	Backend    string
	DryRun     bool
	Executable string
	Paths      Paths
}

type InstallPlan struct {
	Backend string   `json:"backend"`
	Path    string   `json:"path,omitempty"`
	Command []string `json:"command,omitempty"`
	Content string   `json:"content,omitempty"`
}

func PlanInstall(opts InstallOptions) (InstallPlan, error) {
	paths := opts.Paths
	if paths.ConfigPath == "" {
		var err error
		paths, err = DefaultPaths(opts.ConfigPath)
		if err != nil {
			return InstallPlan{}, err
		}
	}
	every := opts.Every
	if strings.TrimSpace(every) == "" {
		every = "10m"
	}
	duration, err := ParseEvery(every)
	if err != nil {
		return InstallPlan{}, err
	}
	exe := opts.Executable
	if strings.TrimSpace(exe) == "" {
		exe, err = os.Executable()
		if err != nil {
			return InstallPlan{}, err
		}
	}
	backend := normalizeBackend(opts.Backend)
	if backend == "auto" {
		backend = defaultBackend()
	}
	args := []string{exe, "--config", paths.ConfigPath, "run"}
	switch backend {
	case "launchd":
		content, err := renderLaunchd(args, paths, duration)
		if err != nil {
			return InstallPlan{}, err
		}
		home, _ := os.UserHomeDir()
		return InstallPlan{Backend: backend, Path: filepath.Join(home, "Library", "LaunchAgents", "org.openclaw.crawlctl.plist"), Content: content}, nil
	case "systemd":
		service, timer, err := renderSystemd(args, duration)
		if err != nil {
			return InstallPlan{}, err
		}
		home, _ := os.UserHomeDir()
		return InstallPlan{Backend: backend, Path: filepath.Join(home, ".config", "systemd", "user", "crawlctl.service"), Content: service + "\n--- crawlctl.timer ---\n" + timer}, nil
	case "windows":
		if duration%time.Minute != 0 {
			return InstallPlan{}, fmt.Errorf("windows scheduler interval must be whole minutes: %s", duration)
		}
		minutes := int(duration / time.Minute)
		tr := quoteWindows(args)
		return InstallPlan{Backend: backend, Command: []string{"schtasks", "/Create", "/TN", "CrawlCtl", "/SC", "MINUTE", "/MO", strconv.Itoa(minutes), "/TR", tr, "/F"}}, nil
	case "cron":
		if duration%time.Minute != 0 {
			return InstallPlan{}, fmt.Errorf("cron interval must be whole minutes: %s", duration)
		}
		minutes := int(duration / time.Minute)
		if minutes < 1 || minutes > 59 {
			return InstallPlan{}, fmt.Errorf("cron interval must be between 1m and 59m: %s", duration)
		}
		line := fmt.Sprintf("*/%d * * * * %s >> %s 2>&1 # crawlctl\n", minutes, shellQuoteArgs(args), shellQuote(filepath.Join(paths.LogDir, "crawlctl-cron.log")))
		return InstallPlan{Backend: backend, Content: line}, nil
	default:
		return InstallPlan{}, fmt.Errorf("unsupported scheduler backend %q", opts.Backend)
	}
}

func Install(opts InstallOptions) (InstallPlan, error) {
	plan, err := PlanInstall(opts)
	if err != nil || opts.DryRun {
		return plan, err
	}
	switch plan.Backend {
	case "launchd":
		if err := os.MkdirAll(filepath.Dir(plan.Path), 0o755); err != nil {
			return plan, err
		}
		if err := os.WriteFile(plan.Path, []byte(plan.Content), 0o644); err != nil {
			return plan, err
		}
		_ = exec.Command("launchctl", "unload", plan.Path).Run()
		if err := exec.Command("launchctl", "load", plan.Path).Run(); err != nil {
			return plan, err
		}
	case "systemd":
		dir := filepath.Dir(plan.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return plan, err
		}
		parts := strings.Split(plan.Content, "\n--- crawlctl.timer ---\n")
		if len(parts) != 2 {
			return plan, fmt.Errorf("invalid systemd plan")
		}
		if err := os.WriteFile(filepath.Join(dir, "crawlctl.service"), []byte(parts[0]), 0o644); err != nil {
			return plan, err
		}
		if err := os.WriteFile(filepath.Join(dir, "crawlctl.timer"), []byte(parts[1]), 0o644); err != nil {
			return plan, err
		}
		if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
			return plan, err
		}
		if err := exec.Command("systemctl", "--user", "enable", "--now", "crawlctl.timer").Run(); err != nil {
			return plan, err
		}
	case "windows":
		if len(plan.Command) == 0 {
			return plan, fmt.Errorf("missing schtasks command")
		}
		if err := exec.Command(plan.Command[0], plan.Command[1:]...).Run(); err != nil {
			return plan, err
		}
	case "cron":
		return plan, fmt.Errorf("cron install is dry-run only; add the printed line with crontab -e")
	}
	return plan, nil
}

func Uninstall(backend string) error {
	backend = normalizeBackend(backend)
	if backend == "auto" {
		backend = defaultBackend()
	}
	switch backend {
	case "launchd":
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, "Library", "LaunchAgents", "org.openclaw.crawlctl.plist")
		_ = exec.Command("launchctl", "unload", path).Run()
		return os.Remove(path)
	case "systemd":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "crawlctl.timer").Run()
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, ".config", "systemd", "user")
		_ = os.Remove(filepath.Join(dir, "crawlctl.service"))
		return os.Remove(filepath.Join(dir, "crawlctl.timer"))
	case "windows":
		return exec.Command("schtasks", "/Delete", "/TN", "CrawlCtl", "/F").Run()
	default:
		return fmt.Errorf("unsupported scheduler backend %q", backend)
	}
}

func defaultBackend() string {
	switch runtime.GOOS {
	case "darwin":
		return "launchd"
	case "windows":
		return "windows"
	default:
		return "systemd"
	}
}

func normalizeBackend(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "auto":
		return "auto"
	case "launchd", "systemd", "cron":
		return value
	case "task", "taskscheduler", "task-scheduler", "windows", "schtasks":
		return "windows"
	default:
		return value
	}
}

func renderLaunchd(args []string, paths Paths, every time.Duration) (string, error) {
	data := map[string]any{
		"Args":   args,
		"Every":  int(every.Seconds()),
		"Stdout": filepath.Join(paths.LogDir, "crawlctl.launchd.out.log"),
		"Stderr": filepath.Join(paths.LogDir, "crawlctl.launchd.err.log"),
	}
	return executeTemplate(launchdTemplate, data)
}

func renderSystemd(args []string, every time.Duration) (string, string, error) {
	service, err := executeTemplate(systemdServiceTemplate, map[string]any{"Command": shellQuoteArgs(args)})
	if err != nil {
		return "", "", err
	}
	timer, err := executeTemplate(systemdTimerTemplate, map[string]any{"Every": durationSystemd(every)})
	return service, timer, err
}

func executeTemplate(body string, data any) (string, error) {
	tmpl, err := template.New("scheduler").Funcs(template.FuncMap{"xml": xmlText}).Parse(body)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func xmlText(value string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(value)); err != nil {
		return value
	}
	return buf.String()
}

func durationSystemd(d time.Duration) string {
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func shellQuoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func quoteWindows(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, " \t\"") {
			quoted[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		} else {
			quoted[i] = arg
		}
	}
	return strings.Join(quoted, " ")
}

const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>org.openclaw.crawlctl</string>
  <key>ProgramArguments</key>
  <array>
{{- range .Args }}
    <string>{{ xml . }}</string>
{{- end }}
  </array>
  <key>StartInterval</key>
  <integer>{{ .Every }}</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{ xml .Stdout }}</string>
  <key>StandardErrorPath</key>
  <string>{{ xml .Stderr }}</string>
</dict>
</plist>
`

const systemdServiceTemplate = `[Unit]
Description=crawlctl crawler refresh

[Service]
Type=oneshot
ExecStart={{ .Command }}
`

const systemdTimerTemplate = `[Unit]
Description=Run crawlctl periodically

[Timer]
OnBootSec=2min
OnUnitActiveSec={{ .Every }}
Unit=crawlctl.service

[Install]
WantedBy=timers.target
`
