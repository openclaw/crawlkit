package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/openclaw/crawlkit/control"
)

type ArchiveSnapshot struct {
	ID                 string   `json:"id"`
	SourceSHA256       string   `json:"source_sha256,omitempty"`
	SchemaName         string   `json:"schema_name,omitempty"`
	SchemaVersion      int      `json:"schema_version,omitempty"`
	SchemaHash         string   `json:"schema_hash,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	SourceSyncAt       string   `json:"source_sync_at,omitempty"`
	DatasetGeneratedAt string   `json:"dataset_generated_at,omitempty"`
	CoverageComplete   bool     `json:"coverage_complete,omitempty"`
	PublishedAt        string   `json:"published_at,omitempty"`
	CutoverAt          string   `json:"cutover_at,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

type ArchivePublish struct {
	Status     string `json:"status"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
}

type Archive struct {
	ID            string           `json:"id"`
	App           string           `json:"app"`
	Slug          string           `json:"slug"`
	SchemaName    string           `json:"schema_name,omitempty"`
	SchemaVersion int              `json:"schema_version,omitempty"`
	SchemaHash    string           `json:"schema_hash,omitempty"`
	Capabilities  []string         `json:"capabilities,omitempty"`
	LastIngestAt  string           `json:"last_ingest_at,omitempty"`
	LastSyncAt    string           `json:"last_sync_at,omitempty"`
	Snapshot      *ArchiveSnapshot `json:"snapshot,omitempty"`
	Publish       *ArchivePublish  `json:"publish,omitempty"`
}

type Status struct {
	App                string            `json:"app"`
	Archive            string            `json:"archive"`
	Mode               string            `json:"mode,omitempty"`
	GeneratedAt        string            `json:"generated_at,omitempty"`
	SchemaName         string            `json:"schema_name,omitempty"`
	SchemaVersion      int               `json:"schema_version,omitempty"`
	SchemaHash         string            `json:"schema_hash,omitempty"`
	LastSyncAt         string            `json:"last_sync_at,omitempty"`
	LastIngestAt       string            `json:"last_ingest_at,omitempty"`
	Counts             []control.Count   `json:"counts,omitempty"`
	Capabilities       []string          `json:"capabilities,omitempty"`
	SQLiteObject       *SQLiteObject     `json:"sqlite_object,omitempty"`
	SQLiteBundle       *SQLiteBundle     `json:"sqlite_bundle,omitempty"`
	SnapshotMode       string            `json:"snapshot_mode,omitempty"`
	SnapshotCutoverAt  string            `json:"snapshot_cutover_at,omitempty"`
	ActiveSnapshotID   string            `json:"active_snapshot_id,omitempty"`
	SourceSyncAt       string            `json:"source_sync_at,omitempty"`
	DatasetGeneratedAt string            `json:"dataset_generated_at,omitempty"`
	CoverageComplete   bool              `json:"coverage_complete,omitempty"`
	Datasets           []DatasetCoverage `json:"datasets,omitempty"`
	Snapshot           *ArchiveSnapshot  `json:"snapshot,omitempty"`
	Publish            *ArchivePublish   `json:"publish,omitempty"`
	Warnings           []string          `json:"warnings,omitempty"`
}

type PublisherStatus struct {
	App              string           `json:"app"`
	Archive          string           `json:"archive"`
	ActiveSnapshotID string           `json:"active_snapshot_id,omitempty"`
	CoverageComplete bool             `json:"coverage_complete,omitempty"`
	Snapshot         *ArchiveSnapshot `json:"snapshot,omitempty"`
}

type DatasetCoverage struct {
	Dataset            string `json:"dataset"`
	RowCount           int64  `json:"row_count,omitempty"`
	EligibleCount      int64  `json:"eligible_count,omitempty"`
	CoveredCount       int64  `json:"covered_count,omitempty"`
	FreshCount         int64  `json:"fresh_count,omitempty"`
	MaxSourceAt        string `json:"max_source_at,omitempty"`
	DatasetGeneratedAt string `json:"dataset_generated_at,omitempty"`
	Complete           bool   `json:"complete,omitempty"`
}

type QueryRequest struct {
	App        string         `json:"app,omitempty"`
	Archive    string         `json:"archive,omitempty"`
	Name       string         `json:"name"`
	Args       map[string]any `json:"args,omitempty"`
	Limit      int            `json:"limit,omitempty"`
	Cursor     string         `json:"cursor,omitempty"`
	SnapshotID string         `json:"snapshot_id,omitempty"`
}

type QueryStats struct {
	RowsRead           int64  `json:"rows_read,omitempty"`
	RowsWritten        int64  `json:"rows_written,omitempty"`
	DurationMS         int64  `json:"duration_ms,omitempty"`
	ServedBy           string `json:"served_by,omitempty"`
	SnapshotID         string `json:"snapshot_id,omitempty"`
	SourceSyncAt       string `json:"source_sync_at,omitempty"`
	DatasetGeneratedAt string `json:"dataset_generated_at,omitempty"`
	CoverageComplete   bool   `json:"coverage_complete,omitempty"`
	SchemaVersion      int    `json:"schema_version,omitempty"`
	ObservationOrder   string `json:"observation_order,omitempty"`
	NextCursor         string `json:"next_cursor,omitempty"`
}

type QueryResult struct {
	Columns    []string         `json:"columns"`
	Rows       [][]any          `json:"rows"`
	Values     []map[string]any `json:"values,omitempty"`
	Cursor     string           `json:"cursor,omitempty"`
	Stats      QueryStats       `json:"stats,omitempty"`
	SchemaHash string           `json:"schema_hash,omitempty"`
	Snapshot   *ArchiveSnapshot `json:"snapshot,omitempty"`
}

type IngestManifest struct {
	App           string   `json:"app"`
	Archive       string   `json:"archive"`
	SchemaName    string   `json:"schema_name,omitempty"`
	SchemaVersion int      `json:"schema_version"`
	SchemaHash    string   `json:"schema_hash"`
	Mode          string   `json:"mode,omitempty"`
	Source        string   `json:"source,omitempty"`
	SourceSyncAt  string   `json:"source_sync_at,omitempty"`
	SnapshotID    string   `json:"snapshot_id,omitempty"`
	SourceSHA256  string   `json:"source_sha256,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

type IngestRequest struct {
	Manifest      IngestManifest `json:"manifest"`
	Table         string         `json:"table"`
	Columns       []string       `json:"columns"`
	Rows          [][]any        `json:"rows"`
	Cursor        string         `json:"cursor,omitempty"`
	MutationToken string         `json:"mutation_token,omitempty"`
	Final         bool           `json:"final,omitempty"`
}

type IngestResult struct {
	RunID           string `json:"run_id,omitempty"`
	Table           string `json:"table,omitempty"`
	SnapshotID      string `json:"snapshot_id,omitempty"`
	MutationToken   string `json:"mutation_token,omitempty"`
	RowsAccepted    int64  `json:"rows_accepted,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	Complete        bool   `json:"complete,omitempty"`
	ResetIncomplete bool   `json:"reset_incomplete,omitempty"`
	ResetDeleted    int64  `json:"reset_deleted,omitempty"`
}

type CutoverResult struct {
	Archive      string `json:"archive,omitempty"`
	SnapshotID   string `json:"snapshot_id"`
	SnapshotMode string `json:"snapshot_mode,omitempty"`
	CutoverAt    string `json:"cutover_at,omitempty"`
}

func (c *Client) Archives(ctx context.Context) ([]Archive, error) {
	var out struct {
		Archives []Archive `json:"archives"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/archives", nil, &out, true)
	return out.Archives, err
}

func (c *Client) Status(ctx context.Context, app, archive string) (Status, error) {
	var out Status
	err := c.do(ctx, http.MethodGet, archivePath(app, archive, "status"), nil, &out, true)
	return out, err
}

func (c *Client) PublishStatus(ctx context.Context, app, archive string) (PublisherStatus, error) {
	return c.publishStatus(ctx, app, archive, "")
}

func (c *Client) PublishStatusForSnapshot(ctx context.Context, app, archive, snapshotID string) (PublisherStatus, error) {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return PublisherStatus{}, errors.New("publish status snapshot id is required")
	}
	return c.publishStatus(ctx, app, archive, snapshotID)
}

func (c *Client) publishStatus(ctx context.Context, app, archive, snapshotID string) (PublisherStatus, error) {
	app = strings.TrimSpace(app)
	archive = strings.TrimSpace(archive)
	var out PublisherStatus
	endpoint, err := url.Parse(c.url(archivePath(app, archive, "publish-status")))
	if err != nil {
		return out, fmt.Errorf("build publish status URL: %w", err)
	}
	if snapshotID != "" {
		query := endpoint.Query()
		query.Set("snapshot_id", snapshotID)
		endpoint.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return out, err
	}
	err = c.doRequest(ctx, req, false, &out, true, true)
	if err == nil && snapshotID != "" {
		if out.App != app || out.Archive != archive {
			return PublisherStatus{}, fmt.Errorf(
				"publish status returned route %q/%q, want %q/%q",
				out.App,
				out.Archive,
				app,
				archive,
			)
		}
		if out.Snapshot == nil {
			return PublisherStatus{}, fmt.Errorf(
				"publish status did not return requested snapshot %q",
				snapshotID,
			)
		}
		if out.Snapshot.ID != snapshotID {
			return PublisherStatus{}, fmt.Errorf(
				"publish status returned snapshot %q, want %q",
				out.Snapshot.ID,
				snapshotID,
			)
		}
	}
	return out, err
}

func (c *Client) Query(ctx context.Context, app, archive string, req QueryRequest) (QueryResult, error) {
	req.App = strings.TrimSpace(app)
	req.Archive = strings.TrimSpace(archive)
	var out QueryResult
	err := c.do(ctx, http.MethodPost, archivePath(app, archive, "query"), req, &out, true)
	return out, err
}

func (c *Client) BatchRead(ctx context.Context, app, archive string, requests []QueryRequest) ([]QueryResult, error) {
	body := struct {
		Requests []QueryRequest `json:"requests"`
	}{Requests: requests}
	for i := range body.Requests {
		body.Requests[i].App = strings.TrimSpace(app)
		body.Requests[i].Archive = strings.TrimSpace(archive)
	}
	var out struct {
		Results []QueryResult `json:"results"`
	}
	err := c.do(ctx, http.MethodPost, archivePath(app, archive, "batch-read"), body, &out, true)
	return out.Results, err
}

func (c *Client) Ingest(ctx context.Context, app, archive string, req IngestRequest) (IngestResult, error) {
	req.Manifest.App = strings.TrimSpace(app)
	req.Manifest.Archive = strings.TrimSpace(archive)
	var out IngestResult
	err := c.do(ctx, http.MethodPost, archivePath(app, archive, "ingest"), req, &out, true)
	return out, err
}

func (c *Client) Cutover(ctx context.Context, app, archive, snapshotID string) (CutoverResult, error) {
	var out CutoverResult
	err := c.do(ctx, http.MethodPost, archivePath(app, archive, "cutover"), struct {
		SnapshotID string `json:"snapshot_id"`
	}{SnapshotID: strings.TrimSpace(snapshotID)}, &out, true)
	return out, err
}

func archivePath(app, archive, action string) string {
	return path.Join(
		"/v1/apps",
		url.PathEscape(strings.TrimSpace(app)),
		"archives",
		url.PathEscape(strings.TrimSpace(archive)),
		strings.TrimSpace(action),
	)
}
