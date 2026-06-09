package releasecheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckReportsNewReleaseAndCaches(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/openclaw/gitcrawl/releases/latest" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": "v0.4.0",
			"html_url": "https://github.com/openclaw/gitcrawl/releases/tag/v0.4.0",
		})
	}))
	defer server.Close()
	oldAPI := GitHubAPI
	GitHubAPI = server.URL
	defer func() { GitHubAPI = oldAPI }()

	now := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	opts := Options{
		AppName:        "gitcrawl",
		Owner:          "openclaw",
		Repo:           "gitcrawl",
		CurrentVersion: "v0.3.2",
		CacheDir:       t.TempDir(),
		Now:            func() time.Time { return now },
		Client:         server.Client(),
	}
	result, err := Check(context.Background(), opts)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "v0.4.0" || result.FromCache {
		t.Fatalf("result = %+v", result)
	}
	if requests != 1 {
		t.Fatalf("requests = %d", requests)
	}

	result, err = Check(context.Background(), opts)
	if err != nil {
		t.Fatalf("Check cached: %v", err)
	}
	if !result.FromCache || !result.UpdateAvailable {
		t.Fatalf("cached result = %+v", result)
	}
	if requests != 1 {
		t.Fatalf("requests after cache = %d", requests)
	}
}

func TestCheckSkipsDevelopmentVersions(t *testing.T) {
	for _, version := range []string{"dev", "ci", "0.0.0-dev", "v0.4.0-dirty"} {
		t.Run(version, func(t *testing.T) {
			result, err := Check(context.Background(), Options{
				AppName:        "gitcrawl",
				Owner:          "openclaw",
				Repo:           "gitcrawl",
				CurrentVersion: version,
				CacheDir:       t.TempDir(),
			})
			if !errors.Is(err, ErrSkipped) {
				t.Fatalf("err = %v", err)
			}
			if !result.Skipped || result.Reason != "development version" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestNotifyWritesOnlyWhenAllowedAndOutdated(t *testing.T) {
	cacheDir := t.TempDir()
	cache := cacheFile{
		CheckedAt:     time.Now(),
		LatestVersion: "v0.9.0",
		LatestURL:     "https://github.com/openclaw/discrawl/releases/tag/v0.9.0",
	}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cacheDir, "releasecheck", "discrawl.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr strings.Builder
	result, err := Notify(context.Background(), NotifyOptions{
		Options: Options{
			AppName:        "discrawl",
			Owner:          "openclaw",
			Repo:           "discrawl",
			CurrentVersion: "v0.8.0",
			CacheDir:       cacheDir,
		},
		Stderr:      &stderr,
		InstallHint: "brew upgrade openclaw/tap/discrawl",
		IsTerminal:  true,
		Getenv:      func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !result.UpdateAvailable || !strings.Contains(stderr.String(), "discrawl: new version available: v0.8.0 -> v0.9.0") {
		t.Fatalf("result=%+v stderr=%q", result, stderr.String())
	}
	if !strings.Contains(stderr.String(), "brew upgrade openclaw/tap/discrawl") {
		t.Fatalf("missing install hint: %q", stderr.String())
	}
}

func TestShouldNotifySkipsScriptedOutput(t *testing.T) {
	tests := []struct {
		name string
		opts NotifyOptions
	}{
		{name: "json", opts: NotifyOptions{JSONOutput: true, IsTerminal: true}},
		{name: "not terminal", opts: NotifyOptions{IsTerminal: false}},
		{name: "ci", opts: NotifyOptions{IsTerminal: true, Getenv: func(key string) string {
			if key == "CI" {
				return "true"
			}
			return ""
		}}},
		{name: "global disabled", opts: NotifyOptions{IsTerminal: true, Getenv: func(key string) string {
			if key == "CRAWLKIT_NO_UPDATE_CHECK" {
				return "1"
			}
			return ""
		}}},
		{name: "app disabled", opts: NotifyOptions{Options: Options{AppName: "gitcrawl"}, IsTerminal: true, Getenv: func(key string) string {
			if key == "GITCRAWL_NO_UPDATE_CHECK" {
				return "1"
			}
			return ""
		}}},
		{name: "metadata", opts: NotifyOptions{IsTerminal: true, Args: []string{"metadata"}}},
		{name: "json flag value", opts: NotifyOptions{IsTerminal: true, Args: []string{"status", "--json=true"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ok, _ := ShouldNotify(tt.opts); ok {
				t.Fatal("ShouldNotify = true")
			}
		})
	}
}

func TestStderrIsTerminalReturnsBool(t *testing.T) {
	_ = StderrIsTerminal()
}

func TestVersionLess(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"v0.3.2", "v0.4.0", true},
		{"0.9.0", "v0.9.0", false},
		{"v1.10.0", "v1.9.9", false},
		{"dev", "v1.0.0", false},
	}
	for _, tt := range tests {
		if got := versionLess(tt.current, tt.latest); got != tt.want {
			t.Fatalf("versionLess(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestStatusText(t *testing.T) {
	upToDate := StatusText("gitcrawl", "", Result{CurrentVersion: "v1.0.0"})
	if !strings.Contains(upToDate, "gitcrawl: up to date (v1.0.0)") {
		t.Fatalf("upToDate = %q", upToDate)
	}
	skipped := StatusText("gitcrawl", "", Result{Skipped: true, Reason: "development version"})
	if !strings.Contains(skipped, "update check skipped: development version") {
		t.Fatalf("skipped = %q", skipped)
	}
}
