package scheduler

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/config"
	"github.com/pelletier/go-toml/v2"
)

const ConfigVersion = 1

type Config struct {
	Version int            `toml:"version" json:"version"`
	Runner  RunnerConfig   `toml:"runner" json:"runner"`
	Jobs    map[string]Job `toml:"jobs" json:"jobs"`
}

type RunnerConfig struct {
	Every       string `toml:"every" json:"every"`
	MaxLogBytes int64  `toml:"max_log_bytes" json:"max_log_bytes"`
}

type Job struct {
	Enabled bool     `toml:"enabled" json:"enabled"`
	Every   string   `toml:"every,omitempty" json:"every,omitempty"`
	Command []string `toml:"command" json:"command"`
	Repos   []string `toml:"repos,omitempty" json:"repos,omitempty"`
	WorkDir string   `toml:"work_dir,omitempty" json:"work_dir,omitempty"`
	Env     []string `toml:"env,omitempty" json:"env,omitempty"`
}

type Paths struct {
	ConfigPath string
	BaseDir    string
	LogDir     string
	StateDir   string
	LockPath   string
	History    string
}

func DefaultPaths(configPath string) (Paths, error) {
	app := config.App{Name: "crawlctl", PlatformDirs: true}
	defaults, err := app.DefaultPaths()
	if err != nil {
		return Paths{}, err
	}
	customConfig := strings.TrimSpace(configPath) != ""
	if strings.TrimSpace(configPath) == "" {
		configPath = defaults.ConfigPath
	} else {
		configPath = config.ExpandHome(configPath)
	}
	base := filepath.Dir(configPath)
	logDir := defaults.LogDir
	stateDir := filepath.Join(defaults.BaseDir, "state")
	if customConfig {
		logDir = filepath.Join(base, "logs")
		stateDir = filepath.Join(base, "state")
	}
	if strings.TrimSpace(logDir) == "" {
		logDir = filepath.Join(base, "logs")
	}
	return Paths{
		ConfigPath: configPath,
		BaseDir:    base,
		LogDir:     logDir,
		StateDir:   stateDir,
		LockPath:   filepath.Join(stateDir, "crawlctl.lock"),
		History:    filepath.Join(stateDir, "runs.jsonl"),
	}, nil
}

func DefaultConfig() Config {
	return Config{
		Version: ConfigVersion,
		Runner: RunnerConfig{
			Every:       "10m",
			MaxLogBytes: 10 * 1024 * 1024,
		},
		Jobs: map[string]Job{},
	}
}

func Load(path string) (Config, Paths, error) {
	paths, err := DefaultPaths(path)
	if err != nil {
		return Config{}, Paths{}, err
	}
	data, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		return Config{}, paths, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	cfg := DefaultConfig()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, paths, fmt.Errorf("parse config: %w", err)
	}
	ApplyDefaults(&cfg)
	return cfg, paths, nil
}

func Save(path string, cfg Config, force bool) (Paths, error) {
	paths, err := DefaultPaths(path)
	if err != nil {
		return Paths{}, err
	}
	ApplyDefaults(&cfg)
	if !force {
		if _, err := os.Stat(paths.ConfigPath); err == nil {
			return paths, fmt.Errorf("config exists: %s", paths.ConfigPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return paths, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath), 0o755); err != nil {
		return paths, err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return paths, err
	}
	if err := os.WriteFile(paths.ConfigPath, data, 0o600); err != nil {
		return paths, err
	}
	return paths, nil
}

func ApplyDefaults(cfg *Config) {
	if cfg.Version == 0 {
		cfg.Version = ConfigVersion
	}
	if strings.TrimSpace(cfg.Runner.Every) == "" {
		cfg.Runner.Every = "10m"
	}
	if cfg.Runner.MaxLogBytes == 0 {
		cfg.Runner.MaxLogBytes = 10 * 1024 * 1024
	}
	if cfg.Jobs == nil {
		cfg.Jobs = map[string]Job{}
	}
	for name, job := range cfg.Jobs {
		if strings.TrimSpace(job.Every) == "" {
			job.Every = cfg.Runner.Every
		}
		cfg.Jobs[name] = job
	}
}

func EnabledJobNames(cfg Config) []string {
	names := make([]string, 0, len(cfg.Jobs))
	for name, job := range cfg.Jobs {
		if job.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func ParseEvery(value string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	if d < time.Minute {
		return 0, fmt.Errorf("duration must be at least 1m")
	}
	return d, nil
}
