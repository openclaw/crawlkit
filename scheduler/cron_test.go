package scheduler

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlanInstallCronRejectsNewlinePaths(t *testing.T) {
	for _, field := range []string{"executable", "config", "logs"} {
		for _, newline := range []struct{ name, value string }{{"LF", "\n"}, {"CRLF", "\r\n"}} {
			t.Run(field+"/"+newline.name, func(t *testing.T) {
				opts := InstallOptions{Backend: "cron", Every: "5m", Executable: "crawlctl", Paths: Paths{ConfigPath: "config.toml", LogDir: "logs"}}
				switch field {
				case "executable":
					opts.Executable = "crawl" + newline.value + "ctl"
				case "config":
					opts.Paths.ConfigPath = "config" + newline.value + ".toml"
				case "logs":
					opts.Paths.LogDir = "logs" + newline.value + "directory"
				}
				plan, err := PlanInstall(opts)
				if err == nil || !strings.Contains(err.Error(), "newlines") || plan.Content != "" {
					t.Fatalf("multiline cron plan = %#v, %v; want refusal without content", plan, err)
				}
			})
		}
	}
}

func TestPlanInstallCronLiteralPaths(t *testing.T) {
	for _, suffix := range []string{"plain", "100%", "two%%", "space '%", `slash\%`, `slashes\\%`} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "crawlctl-"+suffix)
			paths := Paths{ConfigPath: filepath.Join(dir, "config-"+suffix), LogDir: filepath.Join(dir, "logs-"+suffix)}
			plan, err := PlanInstall(InstallOptions{Backend: "cron", Every: "5m", Executable: exe, Paths: paths})
			if err != nil {
				t.Fatal(err)
			}
			field, ok := strings.CutPrefix(plan.Content, "*/5 * * * * ")
			if !ok {
				t.Fatalf("unexpected schedule: %q", plan.Content)
			}
			command := cronShellCommand(t, strings.TrimSuffix(field, "\n"))
			if runtime.GOOS == "windows" {
				return // Cron preprocessing is portable; execution requires a POSIX shell.
			}
			if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(paths.LogDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
				t.Fatalf("run cron command: %v: %s", err, out)
			}
			out, err := os.ReadFile(filepath.Join(paths.LogDir, "crawlctl-cron.log"))
			if want := "--config\n" + paths.ConfigPath + "\nrun\n"; err != nil || string(out) != want {
				t.Fatalf("logged arguments = %q, %v; want %q", out, err, want)
			}
		})
	}
}

// Cron processes percent separators before invoking the shell, ignoring its quotes.
// See https://github.com/cronie-crond/cronie/blob/master/src/do_command.c.
func cronShellCommand(t *testing.T, field string) string {
	t.Helper()
	var command strings.Builder
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case '%':
			t.Fatalf("cron would truncate the command at an unescaped percent: %q", field)
		case '\\':
			if i+1 < len(field) {
				i++
				if field[i] != '%' {
					command.WriteByte('\\')
				}
			}
		}
		command.WriteByte(field[i])
	}
	return command.String()
}
