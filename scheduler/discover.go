package scheduler

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"

	"github.com/openclaw/crawlkit/control"
)

type App struct {
	ID          string            `json:"id"`
	Binary      string            `json:"binary"`
	DisplayName string            `json:"display_name,omitempty"`
	Manifest    *control.Manifest `json:"manifest,omitempty"`
	Legacy      bool              `json:"legacy,omitempty"`
	Found       bool              `json:"found"`
	Path        string            `json:"path,omitempty"`
	Error       string            `json:"error,omitempty"`
}

func Discover(ctx context.Context, binaries []string) []App {
	if len(binaries) == 0 {
		binaries = DefaultBinaries()
	}
	out := make([]App, 0, len(binaries))
	seen := map[string]bool{}
	for _, binary := range binaries {
		binary = strings.TrimSpace(binary)
		if binary == "" || seen[binary] {
			continue
		}
		seen[binary] = true
		out = append(out, discoverOne(ctx, binary))
	}
	return out
}

func DefaultBinaries() []string {
	return []string{"gitcrawl", "discrawl", "notcrawl", "wacrawl", "telecrawl", "slacrawl"}
}

func discoverOne(ctx context.Context, binary string) App {
	path, err := exec.LookPath(binary)
	app := App{ID: binary, Binary: binary}
	if err != nil {
		app.Error = err.Error()
		return app
	}
	app.Found = true
	app.Path = path
	cmd := exec.CommandContext(ctx, path, "metadata", "--json")
	data, err := cmd.Output()
	if err == nil {
		var manifest control.Manifest
		if json.Unmarshal(data, &manifest) == nil && manifest.ID != "" {
			app.ID = manifest.ID
			app.DisplayName = manifest.DisplayName
			app.Manifest = &manifest
			return app
		}
	}
	if legacy, ok := LegacyManifest(binary); ok {
		app.ID = legacy.ID
		app.DisplayName = legacy.DisplayName
		app.Manifest = &legacy
		app.Legacy = true
		app.Error = ""
		return app
	}
	if err != nil {
		app.Error = err.Error()
	}
	return app
}

func LegacyManifest(binary string) (control.Manifest, bool) {
	switch binary {
	case "wacrawl":
		m := control.NewManifest("wacrawl", "WhatsApp Crawl", "wacrawl")
		m.Description = "Local-first WhatsApp Desktop archive crawler."
		m.Capabilities = []string{"doctor", "status", "sync", "search", "backup"}
		m.Privacy = control.Privacy{ContainsPrivateMessages: true, LocalOnlyScopes: []string{"whatsapp-desktop", "sqlite", "encrypted-git-backup"}}
		m.Commands = map[string]control.Command{
			"doctor": {Title: "Doctor", Argv: []string{"wacrawl", "--json", "doctor"}, JSON: true},
			"status": {Title: "Status", Argv: []string{"wacrawl", "--json", "--sync", "never", "status"}, JSON: true},
			"sync":   {Title: "Sync", Argv: []string{"wacrawl", "--json", "sync"}, JSON: true, Mutates: true},
		}
		return m, true
	case "telecrawl":
		m := control.NewManifest("telecrawl", "Telegram Crawl", "telecrawl")
		m.Description = "Local-first Telegram Desktop archive crawler."
		m.Capabilities = []string{"doctor", "status", "sync", "search", "backup"}
		m.Privacy = control.Privacy{ContainsPrivateMessages: true, LocalOnlyScopes: []string{"telegram-desktop", "sqlite", "encrypted-git-backup"}}
		m.Commands = map[string]control.Command{
			"doctor": {Title: "Doctor", Argv: []string{"telecrawl", "--json", "doctor"}, JSON: true},
			"status": {Title: "Status", Argv: []string{"telecrawl", "--json", "status"}, JSON: true},
			"sync":   {Title: "Import", Argv: []string{"telecrawl", "--json", "import"}, JSON: true, Mutates: true},
		}
		return m, true
	case "slacrawl":
		m := control.NewManifest("slacrawl", "Slack Crawl", "slacrawl")
		m.Description = "Local-first Slack archive crawler."
		m.Capabilities = []string{"doctor", "status", "sync", "watch", "search", "git-share"}
		m.Privacy = control.Privacy{ContainsPrivateMessages: true, LocalOnlyScopes: []string{"slack", "desktop-cache", "sqlite", "git-share"}}
		m.Commands = map[string]control.Command{
			"doctor": {Title: "Doctor", Argv: []string{"slacrawl", "--json", "doctor"}, JSON: true},
			"status": {Title: "Status", Argv: []string{"slacrawl", "--json", "status"}, JSON: true},
			"sync":   {Title: "Sync", Argv: []string{"slacrawl", "--json", "sync", "--source", defaultSlackSource(), "--latest-only"}, JSON: true, Mutates: true},
		}
		return m, true
	}
	return control.Manifest{}, false
}

func DefaultJobForApp(app App, repos []string) (Job, bool) {
	id := app.ID
	if id == "" {
		id = app.Binary
	}
	exe := app.Path
	if strings.TrimSpace(exe) == "" {
		exe = app.Binary
	}
	if strings.TrimSpace(exe) == "" {
		exe = id
	}
	switch id {
	case "gitcrawl":
		return Job{Enabled: len(repos) > 0, Every: "5m", Command: []string{exe, "refresh", "{repo}", "--json"}, Repos: append([]string(nil), repos...)}, true
	case "discrawl":
		return Job{Enabled: true, Every: "10m", Command: []string{exe, "--json", "sync", "--update=auto"}}, true
	case "notcrawl":
		return Job{Enabled: true, Every: "15m", Command: []string{exe, "--json", "sync", "--source", "all"}}, true
	case "wacrawl":
		return Job{Enabled: true, Every: "15m", Command: []string{exe, "--json", "sync"}}, true
	case "telecrawl":
		return Job{Enabled: true, Every: "30m", Command: []string{exe, "--json", "import"}}, true
	case "slacrawl":
		return Job{Enabled: true, Every: "10m", Command: []string{exe, "--json", "sync", "--source", defaultSlackSource(), "--latest-only"}}, true
	}
	if app.Manifest != nil {
		if command, ok := app.Manifest.Commands["sync"]; ok && len(command.Argv) > 0 {
			argv := append([]string(nil), command.Argv...)
			if strings.TrimSpace(app.Path) != "" {
				argv[0] = app.Path
			}
			return Job{Enabled: true, Every: "15m", Command: argv}, true
		}
	}
	return Job{}, false
}

func defaultSlackSource() string {
	if runtime.GOOS == "darwin" {
		return "all"
	}
	return "api"
}
