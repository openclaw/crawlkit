package releasecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

const (
	DefaultInterval = 24 * time.Hour
)

var GitHubAPI = "https://api.github.com"

var ErrSkipped = errors.New("release check skipped")

type Options struct {
	AppName        string
	Owner          string
	Repo           string
	CurrentVersion string
	CacheDir       string
	Interval       time.Duration
	Force          bool
	Client         *http.Client
	Now            func() time.Time
}

type NotifyOptions struct {
	Options
	Stderr      io.Writer
	InstallHint string
	Args        []string
	JSONOutput  bool
	IsTerminal  bool
	Getenv      func(string) string
}

type Result struct {
	CheckedAt       time.Time `json:"checked_at"`
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	LatestURL       string    `json:"latest_url,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	FromCache       bool      `json:"from_cache"`
	Skipped         bool      `json:"skipped,omitempty"`
	Reason          string    `json:"reason,omitempty"`
}

type cacheFile struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
	LatestURL     string    `json:"latest_url"`
}

type latestRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

func Check(ctx context.Context, opts Options) (Result, error) {
	opts = normalizeOptions(opts)
	current := normalizeVersion(opts.CurrentVersion)
	result := Result{
		CheckedAt:      opts.Now(),
		CurrentVersion: current,
	}
	if opts.AppName == "" || opts.Owner == "" || opts.Repo == "" {
		result.Skipped = true
		result.Reason = "missing release check metadata"
		return result, ErrSkipped
	}
	lowerCurrent := strings.ToLower(current)
	if current == "" || lowerCurrent == "dev" || lowerCurrent == "ci" || strings.Contains(lowerCurrent, "dev") || strings.Contains(lowerCurrent, "dirty") {
		result.Skipped = true
		result.Reason = "development version"
		return result, ErrSkipped
	}

	if !opts.Force {
		if cached, ok := readCache(opts); ok {
			age := opts.Now().Sub(cached.CheckedAt)
			if age >= 0 && age < opts.Interval {
				result.CheckedAt = cached.CheckedAt
				result.LatestVersion = normalizeVersion(cached.LatestVersion)
				result.LatestURL = cached.LatestURL
				result.FromCache = true
				result.UpdateAvailable = versionLess(current, result.LatestVersion)
				return result, nil
			}
		}
	}

	latest, err := fetchLatest(ctx, opts)
	if err != nil {
		return result, err
	}
	result.CheckedAt = opts.Now()
	result.LatestVersion = normalizeVersion(latest.TagName)
	result.LatestURL = latest.HTMLURL
	result.UpdateAvailable = versionLess(current, result.LatestVersion)
	writeCache(opts, cacheFile{
		CheckedAt:     result.CheckedAt,
		LatestVersion: result.LatestVersion,
		LatestURL:     result.LatestURL,
	})
	return result, nil
}

func Notify(ctx context.Context, opts NotifyOptions) (Result, error) {
	if ok, reason := ShouldNotify(opts); !ok {
		result := Result{
			CheckedAt:      normalizeOptions(opts.Options).Now(),
			CurrentVersion: normalizeVersion(opts.CurrentVersion),
			Skipped:        true,
			Reason:         reason,
		}
		return result, ErrSkipped
	}
	result, err := Check(ctx, opts.Options)
	if err != nil {
		return result, err
	}
	if result.UpdateAvailable && opts.Stderr != nil {
		_, _ = io.WriteString(opts.Stderr, Notice(opts.AppName, opts.InstallHint, result))
	}
	return result, nil
}

func ShouldNotify(opts NotifyOptions) (bool, string) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if opts.JSONOutput {
		return false, "json output"
	}
	if !opts.IsTerminal {
		return false, "stderr is not a terminal"
	}
	if envTruthy(getenv("CI")) {
		return false, "ci environment"
	}
	if envTruthy(getenv("CRAWLKIT_NO_UPDATE_CHECK")) {
		return false, "disabled by CRAWLKIT_NO_UPDATE_CHECK"
	}
	prefix := envPrefix(opts.AppName)
	if prefix != "" && envTruthy(getenv(prefix+"_NO_UPDATE_CHECK")) {
		return false, "disabled by " + prefix + "_NO_UPDATE_CHECK"
	}
	if len(opts.Args) > 0 && (opts.Args[0] == "metadata" || opts.Args[0] == "check-update") {
		return false, opts.Args[0] + " command"
	}
	for _, arg := range opts.Args {
		if arg == "--json" || strings.HasPrefix(arg, "--json=") {
			return false, "json output"
		}
	}
	return true, ""
}

func StderrIsTerminal() bool {
	return isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
}

func Notice(appName, installHint string, result Result) string {
	app := strings.TrimSpace(appName)
	if app == "" {
		app = "app"
	}
	current := displayVersion(result.CurrentVersion)
	latest := displayVersion(result.LatestVersion)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: new version available: %s -> %s\n", app, current, latest)
	if strings.TrimSpace(installHint) != "" {
		fmt.Fprintf(&b, "upgrade: %s\n", strings.TrimSpace(installHint))
	} else if strings.TrimSpace(result.LatestURL) != "" {
		fmt.Fprintf(&b, "release: %s\n", strings.TrimSpace(result.LatestURL))
	}
	return b.String()
}

func StatusText(appName, installHint string, result Result) string {
	if result.UpdateAvailable {
		return Notice(appName, installHint, result)
	}
	app := strings.TrimSpace(appName)
	if app == "" {
		app = "app"
	}
	if result.Skipped {
		return fmt.Sprintf("%s: update check skipped: %s\n", app, result.Reason)
	}
	return fmt.Sprintf("%s: up to date (%s)\n", app, displayVersion(result.CurrentVersion))
}

func normalizeOptions(opts Options) Options {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Client == nil {
		opts.Client = http.DefaultClient
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

func fetchLatest(ctx context.Context, opts Options) (latestRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", GitHubAPI, opts.Owner, opts.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return latestRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", opts.AppName+" releasecheck")
	resp, err := opts.Client.Do(req)
	if err != nil {
		return latestRelease{}, fmt.Errorf("check latest %s/%s release: %w", opts.Owner, opts.Repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return latestRelease{}, fmt.Errorf("check latest %s/%s release: github returned %s", opts.Owner, opts.Repo, resp.Status)
	}
	var latest latestRelease
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		return latestRelease{}, fmt.Errorf("parse latest %s/%s release: %w", opts.Owner, opts.Repo, err)
	}
	if strings.TrimSpace(latest.TagName) == "" {
		return latestRelease{}, fmt.Errorf("latest %s/%s release has no tag", opts.Owner, opts.Repo)
	}
	return latest, nil
}

func readCache(opts Options) (cacheFile, bool) {
	path := cachePath(opts)
	if path == "" {
		return cacheFile{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheFile{}, false
	}
	var cached cacheFile
	if err := json.Unmarshal(data, &cached); err != nil || cached.CheckedAt.IsZero() {
		return cacheFile{}, false
	}
	return cached, true
}

func writeCache(opts Options, cached cacheFile) {
	path := cachePath(opts)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0o600)
}

func cachePath(opts Options) string {
	dir := strings.TrimSpace(opts.CacheDir)
	if dir == "" {
		return ""
	}
	name := strings.TrimSpace(opts.AppName)
	if name == "" {
		name = strings.TrimSpace(opts.Repo)
	}
	if name == "" {
		return ""
	}
	return filepath.Join(dir, "releasecheck", name+".json")
}

func versionLess(current, latest string) bool {
	curParts, curPre, curOK := versionParts(current)
	latParts, latPre, latOK := versionParts(latest)
	if !curOK || !latOK {
		return false
	}
	for i := 0; i < 3; i++ {
		if curParts[i] < latParts[i] {
			return true
		}
		if curParts[i] > latParts[i] {
			return false
		}
	}
	if curPre == latPre {
		return false
	}
	if curPre == "" {
		return false
	}
	if latPre == "" {
		return true
	}
	return prereleaseLess(curPre, latPre)
}

var versionRE = regexp.MustCompile(`(?i)^v?([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func versionParts(v string) ([3]int, string, bool) {
	var out [3]int
	match := versionRE.FindStringSubmatch(strings.TrimSpace(v))
	if match == nil {
		return out, "", false
	}
	for i := 1; i <= 3; i++ {
		if match[i] == "" {
			continue
		}
		n, err := strconv.Atoi(match[i])
		if err != nil {
			return out, "", false
		}
		out[i-1] = n
	}
	return out, match[4], true
}

func prereleaseLess(current, latest string) bool {
	curParts := strings.Split(current, ".")
	latParts := strings.Split(latest, ".")
	for i := 0; i < len(curParts) && i < len(latParts); i++ {
		curNum, curErr := strconv.Atoi(curParts[i])
		latNum, latErr := strconv.Atoi(latParts[i])
		switch {
		case curErr == nil && latErr == nil:
			if curNum != latNum {
				return curNum < latNum
			}
		case curErr == nil:
			return true
		case latErr == nil:
			return false
		default:
			if curParts[i] != latParts[i] {
				return curParts[i] < latParts[i]
			}
		}
	}
	return len(curParts) < len(latParts)
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "version ")
	return strings.TrimPrefix(v, "Version ")
}

func displayVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return v
}

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envPrefix(appName string) string {
	appName = strings.ToUpper(strings.TrimSpace(appName))
	appName = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, appName)
	return strings.Trim(appName, "_")
}
