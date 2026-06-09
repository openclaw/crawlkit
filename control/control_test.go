package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestDefaultsSchemaAndBinary(t *testing.T) {
	manifest := NewManifest("slacrawl", "Slack Crawl", "slacrawl")
	if manifest.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %q", manifest.SchemaVersion)
	}
	if manifest.Binary.Name != "slacrawl" {
		t.Fatalf("binary = %#v", manifest.Binary)
	}
	if manifest.Commands == nil {
		t.Fatal("commands map should be initialized")
	}
}

func TestStatusAndRemoteDatabaseDefaults(t *testing.T) {
	status := NewStatus(" gitcrawl ", " ready ")
	if status.SchemaVersion != SchemaVersion || status.AppID != "gitcrawl" || status.State != "unknown" || status.Summary != "ready" {
		t.Fatalf("status = %#v", status)
	}
	if status.GeneratedAt == "" {
		t.Fatal("generated_at should be set")
	}

	counts := []Count{NewCount("threads", "Threads", 3)}
	db := RemoteDatabase(" cloud ", " Cloud archive ", "", "", " https://remote.example/ ", " gitcrawl/openclaw ", true, counts)
	if db.ID != "cloud" || db.Label != "Cloud archive" || db.Kind != "remote" || db.Role != "archive" {
		t.Fatalf("remote database defaults = %#v", db)
	}
	if db.Endpoint != "https://remote.example" || db.Archive != "gitcrawl/openclaw" || !db.IsPrimary {
		t.Fatalf("remote database routing = %#v", db)
	}
	counts[0].Value = 99
	if db.Counts[0].Value != 3 {
		t.Fatalf("counts should be copied: %#v", db.Counts)
	}
}

func TestSQLiteDatabaseStatsPathReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.db")
	if err := os.WriteFile(path, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := SQLiteDatabase("primary", "Primary archive", "archive", path, true, []Count{NewCount("messages", "Messages", 7)})
	if db.Kind != "sqlite" || !db.IsPrimary || db.Bytes != 6 {
		t.Fatalf("unexpected database: %#v", db)
	}
	if db.ModifiedAt == "" {
		t.Fatal("modified_at should be set for existing paths")
	}
	if len(db.Counts) != 1 || db.Counts[0].Value != 7 {
		t.Fatalf("counts = %#v", db.Counts)
	}
}
