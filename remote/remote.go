package remote

import (
	"strings"
)

const (
	ModeLocal     = "local"
	ModeGit       = "git"
	ModeCloud     = "cloud"
	ModeHybrid    = "hybrid"
	ModePublisher = "publisher"

	DefaultTokenEnv                = "CRAWL_REMOTE_TOKEN"
	maxSQLiteBundleMetadataBytes   = 1024
	maxSQLiteBundleSafeInteger     = int64(1<<53 - 1)
	snapshotSQLiteReconstructSteps = "concatenate parts in index order to archive.db.gz, then gzip-decompress to archive.db"
)

type Config struct {
	Mode       string     `toml:"mode" json:"mode"`
	Endpoint   string     `toml:"endpoint" json:"endpoint"`
	Archive    string     `toml:"archive" json:"archive"`
	TokenEnv   string     `toml:"token_env" json:"token_env"`
	StaleAfter string     `toml:"stale_after" json:"stale_after"`
	Auth       AuthConfig `toml:"auth" json:"auth"`
}

type AuthConfig struct {
	TokenSource    string `toml:"token_source" json:"token_source"`
	KeyringService string `toml:"keyring_service" json:"keyring_service"`
	KeyringAccount string `toml:"keyring_account" json:"keyring_account"`
}

func (c *Config) Normalize() {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = ModeLocal
	}
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	c.Archive = strings.TrimSpace(c.Archive)
	c.TokenEnv = strings.TrimSpace(c.TokenEnv)
	if c.TokenEnv == "" {
		c.TokenEnv = DefaultTokenEnv
	}
	c.StaleAfter = strings.TrimSpace(c.StaleAfter)
	c.Auth.TokenSource = strings.ToLower(strings.TrimSpace(c.Auth.TokenSource))
	c.Auth.KeyringService = strings.TrimSpace(c.Auth.KeyringService)
	c.Auth.KeyringAccount = strings.TrimSpace(c.Auth.KeyringAccount)
}

func (c Config) Enabled() bool {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	return mode == ModeCloud || mode == ModeHybrid || mode == ModePublisher
}

func NewClientFromConfig(cfg Config, opts Options) (*Client, error) {
	cfg.Normalize()
	if opts.Endpoint == "" {
		opts.Endpoint = cfg.Endpoint
	}
	if opts.TokenProvider == nil {
		opts.TokenProvider = EnvTokenProvider{Name: cfg.TokenEnv}
	}
	return NewClient(opts)
}
