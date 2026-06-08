package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultPathsUseConfigDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	paths, err := (App{Name: "thingcrawl"}).DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	wantBase := filepath.Join(home, ".config", "thingcrawl")
	if paths.BaseDir != wantBase {
		t.Fatalf("base dir = %q, want %q", paths.BaseDir, wantBase)
	}
	if paths.ConfigPath != filepath.Join(wantBase, "config.toml") {
		t.Fatalf("config path = %q", paths.ConfigPath)
	}
}

func TestPlatformDefaultPathsUseXDGDirs(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "xdg-config")
	dataHome := filepath.Join(home, "xdg-data")
	cacheHome := filepath.Join(home, "xdg-cache")
	stateHome := filepath.Join(home, "xdg-state")
	setTestHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	app := App{Name: "thingcrawl", PlatformDirs: true}
	paths, err := app.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != filepath.Join(configHome, "thingcrawl", "config.toml") {
		t.Fatalf("config path = %q", paths.ConfigPath)
	}
	if paths.DBPath != filepath.Join(dataHome, "thingcrawl", "thingcrawl.db") {
		t.Fatalf("db path = %q", paths.DBPath)
	}
	if paths.CacheDir != filepath.Join(cacheHome, "thingcrawl") {
		t.Fatalf("cache dir = %q", paths.CacheDir)
	}
	if paths.LogDir != filepath.Join(stateHome, "thingcrawl", "logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
	if paths.ShareDir != filepath.Join(dataHome, "thingcrawl", "share") {
		t.Fatalf("share dir = %q", paths.ShareDir)
	}
}

func TestPlatformDefaultPathsUsePlatformFallbacks(t *testing.T) {
	home := t.TempDir()
	configHome, dataHome, cacheHome, stateHome := defaultPlatformTestDirs(home)
	setTestHome(t, home)
	clearXDGEnv(t)

	app := App{Name: "thingcrawl", PlatformDirs: true}
	paths, err := app.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != filepath.Join(configHome, "thingcrawl", "config.toml") {
		t.Fatalf("config path = %q", paths.ConfigPath)
	}
	if paths.DBPath != filepath.Join(dataHome, "thingcrawl", "thingcrawl.db") {
		t.Fatalf("db path = %q", paths.DBPath)
	}
	if paths.CacheDir != filepath.Join(cacheHome, "thingcrawl") {
		t.Fatalf("cache dir = %q", paths.CacheDir)
	}
	if paths.LogDir != filepath.Join(stateHome, "thingcrawl", "logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
}

func TestPlatformDefaultPathsIgnoreRelativeXDGDirs(t *testing.T) {
	home := t.TempDir()
	configHome, dataHome, cacheHome, stateHome := defaultPlatformTestDirs(home)
	setTestHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	t.Setenv("XDG_DATA_HOME", "relative-data")
	t.Setenv("XDG_CACHE_HOME", "relative-cache")
	t.Setenv("XDG_STATE_HOME", "relative-state")

	app := App{Name: "thingcrawl", PlatformDirs: true}
	paths, err := app.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != filepath.Join(configHome, "thingcrawl", "config.toml") {
		t.Fatalf("config path = %q", paths.ConfigPath)
	}
	if paths.DBPath != filepath.Join(dataHome, "thingcrawl", "thingcrawl.db") {
		t.Fatalf("db path = %q", paths.DBPath)
	}
	if paths.CacheDir != filepath.Join(cacheHome, "thingcrawl") {
		t.Fatalf("cache dir = %q", paths.CacheDir)
	}
	if paths.LogDir != filepath.Join(stateHome, "thingcrawl", "logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
}

func TestPlatformDefaultPathsPreferExistingLegacyInstallPaths(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".thingcrawl")
	if err := os.MkdirAll(filepath.Join(legacy, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacy, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacy, "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "thingcrawl.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)
	clearXDGEnv(t)

	app := App{Name: "thingcrawl", PlatformDirs: true, LegacyBaseDir: "~/.thingcrawl"}
	paths, err := app.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != filepath.Join(legacy, "config.toml") {
		t.Fatalf("config path = %q", paths.ConfigPath)
	}
	if paths.DBPath != filepath.Join(legacy, "thingcrawl.db") {
		t.Fatalf("db path = %q", paths.DBPath)
	}
	if paths.CacheDir != filepath.Join(legacy, "cache") {
		t.Fatalf("cache dir = %q", paths.CacheDir)
	}
	if paths.LogDir != filepath.Join(legacy, "logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
	if paths.ShareDir != filepath.Join(legacy, "share") {
		t.Fatalf("share dir = %q", paths.ShareDir)
	}
}

func TestPlatformDefaultPathsMixLegacyAndNewLocations(t *testing.T) {
	home := t.TempDir()
	_, dataHome, _, stateHome := defaultPlatformTestDirs(home)
	legacy := filepath.Join(home, ".thingcrawl")
	newDBPath := filepath.Join(dataHome, "thingcrawl", "thingcrawl.db")
	if err := os.MkdirAll(filepath.Dir(newDBPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newDBPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacy, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacy, "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)
	clearXDGEnv(t)

	app := App{Name: "thingcrawl", PlatformDirs: true, LegacyBaseDir: "~/.thingcrawl"}
	paths, err := app.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.DBPath != newDBPath {
		t.Fatalf("db path = %q", paths.DBPath)
	}
	if paths.CacheDir != filepath.Join(legacy, "cache") {
		t.Fatalf("cache dir = %q", paths.CacheDir)
	}
	if paths.LogDir != filepath.Join(stateHome, "thingcrawl", "logs") {
		t.Fatalf("log dir = %q", paths.LogDir)
	}
	if paths.ShareDir != filepath.Join(legacy, "share") {
		t.Fatalf("share dir = %q", paths.ShareDir)
	}
}

func TestPlatformResolveConfigPathPrefersNewConfigOverLegacy(t *testing.T) {
	home := t.TempDir()
	configHome, _, _, _ := defaultPlatformTestDirs(home)
	legacyConfig := filepath.Join(home, ".thingcrawl", "config.toml")
	newConfig := filepath.Join(configHome, "thingcrawl", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacyConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, home)
	clearXDGEnv(t)

	path, err := (App{Name: "thingcrawl", PlatformDirs: true, LegacyBaseDir: "~/.thingcrawl"}).ResolveConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	if path != newConfig {
		t.Fatalf("config path = %q", path)
	}
}

func TestResolveConfigPathPrecedence(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv("THINGCRAWL_CONFIG", "~/custom/config.toml")

	app := App{Name: "thingcrawl"}
	path, err := app.ResolveConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "custom", "config.toml") {
		t.Fatalf("env path = %q", path)
	}

	path, err = app.ResolveConfigPath("~/flag/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "flag", "config.toml") {
		t.Fatalf("flag path = %q", path)
	}
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
}

func clearXDGEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
}

func defaultPlatformTestDirs(home string) (configHome, dataHome, cacheHome, stateHome string) {
	switch runtime.GOOS {
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support")
		return appSupport, appSupport, filepath.Join(home, "Library", "Caches"), appSupport
	case "windows":
		localAppData := filepath.Join(home, "AppData", "Local")
		return localAppData, localAppData, filepath.Join(localAppData, "cache"), localAppData
	default:
		return filepath.Join(home, ".config"),
			filepath.Join(home, ".local", "share"),
			filepath.Join(home, ".cache"),
			filepath.Join(home, ".local", "state")
	}
}

func TestRuntimeConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := RuntimeConfig{Version: 1, DBPath: filepath.Join(dir, "x.db")}
	if err := WriteTOML(path, cfg, 0); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		got := info.Mode().Perm()
		t.Fatalf("mode = %o", got)
	}
	var loaded RuntimeConfig
	if err := LoadTOML(path, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.DBPath != cfg.DBPath {
		t.Fatalf("loaded db path = %q", loaded.DBPath)
	}
}

func TestRuntimeDefaultsFillAndCreateDirs(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	base := filepath.Join(home, "runtime")
	defaults, err := (App{Name: "thingcrawl", BaseDir: base}).DefaultRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg := RuntimeConfig{
		DBPath: filepath.Join(base, "custom.db"),
	}
	ApplyRuntimeDefaults(&cfg, defaults)
	if cfg.Version != 1 {
		t.Fatalf("version = %d", cfg.Version)
	}
	if cfg.DBPath != filepath.Join(base, "custom.db") {
		t.Fatalf("db path = %q", cfg.DBPath)
	}
	if cfg.CacheDir != filepath.Join(base, "cache") || cfg.LogDir != filepath.Join(base, "logs") || cfg.ShareDir != filepath.Join(base, "share") {
		t.Fatalf("runtime dirs = %#v", cfg)
	}
	if err := EnsureRuntimeDirs(cfg); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(cfg.DBPath), cfg.CacheDir, cfg.LogDir, cfg.ShareDir} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("runtime dir %s: info=%v err=%v", path, info, err)
		}
	}
}

func TestTokenDiagnosticDoesNotExposeValue(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "super-secret")
	got := TokenDiagnosticForEnv("SECRET_TOKEN")
	if !got.Present || got.Source != "env" {
		t.Fatalf("diagnostic = %+v", got)
	}
}
