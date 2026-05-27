package control

import (
	"os"
	"strings"
	"time"
)

const SchemaVersion = "crawlkit.control.v1"

type Manifest struct {
	SchemaVersion string             `json:"schema_version"`
	ID            string             `json:"id"`
	DisplayName   string             `json:"display_name"`
	Description   string             `json:"description,omitempty"`
	Binary        Binary             `json:"binary"`
	Branding      Branding           `json:"branding"`
	Paths         Paths              `json:"paths"`
	Commands      map[string]Command `json:"commands"`
	Capabilities  []string           `json:"capabilities,omitempty"`
	Privacy       Privacy            `json:"privacy"`
}

type Binary struct {
	Name string `json:"name"`
}

type Branding struct {
	SymbolName       string `json:"symbol_name,omitempty"`
	AccentColor      string `json:"accent_color,omitempty"`
	IconPath         string `json:"icon_path,omitempty"`
	BundleIdentifier string `json:"bundle_identifier,omitempty"`
}

type Paths struct {
	DefaultConfig   string `json:"default_config,omitempty"`
	ConfigEnv       string `json:"config_env,omitempty"`
	DefaultDatabase string `json:"default_database,omitempty"`
	DefaultCache    string `json:"default_cache,omitempty"`
	DefaultLogs     string `json:"default_logs,omitempty"`
	DefaultShare    string `json:"default_share,omitempty"`
}

type Command struct {
	Title      string   `json:"title,omitempty"`
	Argv       []string `json:"argv"`
	JSON       bool     `json:"json,omitempty"`
	Mutates    bool     `json:"mutates,omitempty"`
	Legacy     bool     `json:"legacy,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
}

type Privacy struct {
	ContainsPrivateMessages bool     `json:"contains_private_messages"`
	ExportsSecrets          bool     `json:"exports_secrets"`
	LocalOnlyScopes         []string `json:"local_only_scopes,omitempty"`
}

type Status struct {
	SchemaVersion string     `json:"schema_version"`
	AppID         string     `json:"app_id"`
	GeneratedAt   string     `json:"generated_at"`
	State         string     `json:"state"`
	Summary       string     `json:"summary"`
	ConfigPath    string     `json:"config_path,omitempty"`
	DatabasePath  string     `json:"database_path,omitempty"`
	DatabaseBytes int64      `json:"database_bytes,omitempty"`
	WALBytes      int64      `json:"wal_bytes,omitempty"`
	LastSyncAt    string     `json:"last_sync_at,omitempty"`
	LastImportAt  string     `json:"last_import_at,omitempty"`
	LastExportAt  string     `json:"last_export_at,omitempty"`
	Counts        []Count    `json:"counts,omitempty"`
	Freshness     *Freshness `json:"freshness,omitempty"`
	Share         *Share     `json:"share,omitempty"`
	Remote        *Remote    `json:"remote,omitempty"`
	Databases     []Database `json:"databases,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
	Errors        []string   `json:"errors,omitempty"`
}

type Count struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value int64  `json:"value"`
}

type Freshness struct {
	Status            string `json:"status"`
	AgeSeconds        int64  `json:"age_seconds,omitempty"`
	StaleAfterSeconds int64  `json:"stale_after_seconds,omitempty"`
}

type Share struct {
	Enabled     bool   `json:"enabled"`
	RepoPath    string `json:"repo_path,omitempty"`
	Remote      string `json:"remote,omitempty"`
	Branch      string `json:"branch,omitempty"`
	NeedsUpdate bool   `json:"needs_update,omitempty"`
}

type Remote struct {
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Archive      string `json:"archive,omitempty"`
	LastIngestAt string `json:"last_ingest_at,omitempty"`
	LastSyncAt   string `json:"last_sync_at,omitempty"`
	NeedsUpdate  bool   `json:"needs_update,omitempty"`
}

type Database struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	Role       string  `json:"role"`
	Path       string  `json:"path"`
	Endpoint   string  `json:"endpoint,omitempty"`
	Archive    string  `json:"archive,omitempty"`
	IsPrimary  bool    `json:"is_primary"`
	Bytes      int64   `json:"bytes"`
	ModifiedAt string  `json:"modified_at,omitempty"`
	Counts     []Count `json:"counts,omitempty"`
}

func NewManifest(id, displayName, binaryName string) Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		ID:            strings.TrimSpace(id),
		DisplayName:   strings.TrimSpace(displayName),
		Binary:        Binary{Name: strings.TrimSpace(binaryName)},
		Commands:      map[string]Command{},
	}
}

func NewStatus(appID, summary string) Status {
	return Status{
		SchemaVersion: SchemaVersion,
		AppID:         strings.TrimSpace(appID),
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		State:         "unknown",
		Summary:       strings.TrimSpace(summary),
	}
}

func NewCount(id, label string, value int64) Count {
	return Count{ID: strings.TrimSpace(id), Label: strings.TrimSpace(label), Value: value}
}

func SQLiteDatabase(id, label, role, path string, primary bool, counts []Count) Database {
	db := Database{
		ID:        strings.TrimSpace(id),
		Label:     strings.TrimSpace(label),
		Kind:      "sqlite",
		Role:      strings.TrimSpace(role),
		Path:      strings.TrimSpace(path),
		IsPrimary: primary,
		Counts:    append([]Count(nil), counts...),
	}
	if db.Role == "" {
		db.Role = "archive"
	}
	if info, err := os.Stat(db.Path); err == nil {
		db.Bytes = info.Size()
		db.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
	}
	return db
}

func RemoteDatabase(id, label, role, kind, endpoint, archive string, primary bool, counts []Count) Database {
	db := Database{
		ID:        strings.TrimSpace(id),
		Label:     strings.TrimSpace(label),
		Kind:      strings.TrimSpace(kind),
		Role:      strings.TrimSpace(role),
		Endpoint:  strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		Archive:   strings.TrimSpace(archive),
		IsPrimary: primary,
		Counts:    append([]Count(nil), counts...),
	}
	if db.Kind == "" {
		db.Kind = "remote"
	}
	if db.Role == "" {
		db.Role = "archive"
	}
	return db
}
