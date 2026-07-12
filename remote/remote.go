package remote

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openclaw/crawlkit/control"
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

type TokenProvider interface {
	Token(context.Context) (string, error)
}

type StaticToken string

func (t StaticToken) Token(context.Context) (string, error) {
	token := strings.TrimSpace(string(t))
	if token == "" {
		return "", ErrMissingToken
	}
	return token, nil
}

type EnvTokenProvider struct {
	Name string
}

func (p EnvTokenProvider) Token(context.Context) (string, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = DefaultTokenEnv
	}
	token := strings.TrimSpace(os.Getenv(name))
	if token == "" {
		return "", fmt.Errorf("%w: %s", ErrMissingToken, name)
	}
	return token, nil
}

type ChainTokenProvider []TokenProvider

func (p ChainTokenProvider) Token(ctx context.Context) (string, error) {
	var lastErr error
	for _, provider := range p {
		if provider == nil {
			continue
		}
		token, err := provider.Token(ctx)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrMissingToken
}

var ErrMissingToken = errors.New("remote token is missing")

type Options struct {
	Endpoint      string
	HTTPClient    *http.Client
	TokenProvider TokenProvider
	UserAgent     string
}

type Client struct {
	endpoint      *url.URL
	httpClient    *http.Client
	tokenProvider TokenProvider
	userAgent     string
}

func NewClient(opts Options) (*Client, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(opts.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("remote endpoint is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid remote endpoint %q", endpoint)
	}
	if opts.TokenProvider != nil && parsed.Scheme != "https" && !isLocalHTTPHost(parsed.Hostname()) {
		return nil, fmt.Errorf("remote endpoint %q cannot use bearer auth over %s", endpoint, parsed.Scheme)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	userAgent := strings.TrimSpace(opts.UserAgent)
	if userAgent == "" {
		userAgent = "crawlkit-remote"
	}
	return &Client{
		endpoint:      parsed,
		httpClient:    client,
		tokenProvider: opts.TokenProvider,
		userAgent:     userAgent,
	}, nil
}

func isLocalHTTPHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
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

type Identity struct {
	Owner string   `json:"owner"`
	Org   string   `json:"org"`
	Login string   `json:"login,omitempty"`
	Auth  string   `json:"auth,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

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

type SQLiteUploadRequest struct {
	Body          io.Reader
	Size          int64
	ContentSHA256 string
	SchemaName    string
	SchemaVersion int
	SchemaHash    string
	SourceSyncAt  string
}

type SQLiteUploadResult struct {
	App      string        `json:"app,omitempty"`
	Archive  string        `json:"archive,omitempty"`
	Complete bool          `json:"complete,omitempty"`
	Object   *SQLiteObject `json:"object,omitempty"`
}

type SQLiteObject struct {
	Key         string `json:"key,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ETag        string `json:"etag,omitempty"`
	UploadedAt  string `json:"uploaded_at,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

type SQLiteBundleUploadResult struct {
	App      string        `json:"app,omitempty"`
	Archive  string        `json:"archive,omitempty"`
	Complete bool          `json:"complete,omitempty"`
	Bundle   *SQLiteBundle `json:"bundle,omitempty"`
}

type SQLiteBundle struct {
	Key         string                `json:"key,omitempty"`
	Size        int64                 `json:"size,omitempty"`
	ETag        string                `json:"etag,omitempty"`
	UploadedAt  string                `json:"uploaded_at,omitempty"`
	ContentType string                `json:"content_type,omitempty"`
	Manifest    *SQLiteBundleManifest `json:"manifest,omitempty"`
}

type LoginStartRequest struct {
	PollSecretHash string `json:"pollSecretHash"`
}

type LoginStartResult struct {
	LoginID   string `json:"loginID"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

type LoginPollRequest struct {
	LoginID    string `json:"loginID"`
	PollSecret string `json:"pollSecret"`
}

type GitHubTokenLoginRequest struct {
	Token string `json:"token"`
}

type LoginPollResult struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Org    string `json:"org,omitempty"`
	Login  string `json:"login,omitempty"`
	Error  string `json:"error,omitempty"`
}

func NewLoginPollSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create login poll secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func LoginPollSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return fmt.Sprintf("%x", sum[:])
}

func (c *Client) Whoami(ctx context.Context) (Identity, error) {
	var out Identity
	err := c.do(ctx, http.MethodGet, "/v1/whoami", nil, &out, true)
	return out, err
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
	err = c.doRequest(ctx, req, false, &out, true)
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

func (c *Client) UploadSQLite(ctx context.Context, app, archive string, upload SQLiteUploadRequest) (SQLiteUploadResult, error) {
	if upload.Body == nil {
		return SQLiteUploadResult{}, errors.New("sqlite upload body is required")
	}
	headers := http.Header{}
	headers.Set("content-type", "application/vnd.sqlite3")
	setHeader(headers, "x-crawl-schema-name", upload.SchemaName)
	setHeader(headers, "x-crawl-schema-version", intHeader(upload.SchemaVersion))
	setHeader(headers, "x-crawl-schema-hash", upload.SchemaHash)
	setHeader(headers, "x-crawl-source-sync-at", upload.SourceSyncAt)
	setHeader(headers, "x-crawl-content-sha256", upload.ContentSHA256)
	var out SQLiteUploadResult
	err := c.doRaw(ctx, http.MethodPut, archivePath(app, archive, "sqlite"), upload.Body, upload.Size, headers, &out, true)
	return out, err
}

func (c *Client) UploadSQLiteBundlePart(ctx context.Context, app, archive string, part SQLiteBundlePartUpload) (SQLiteUploadResult, error) {
	if part.Body == nil {
		return SQLiteUploadResult{}, errors.New("sqlite bundle part body is required")
	}
	if part.SnapshotID != "" && !validSQLiteSnapshotID(part.SnapshotID) {
		return SQLiteUploadResult{}, errors.New("sqlite bundle snapshot id must be empty or a lowercase sha256 digest")
	}
	snapshotScoped := part.SnapshotID != ""
	if err := validateSQLiteBundlePartLimit(part.Index, part.Size, snapshotScoped); err != nil {
		return SQLiteUploadResult{}, err
	}
	body := part.Body
	if snapshotScoped {
		bounded, err := sqliteBundleDeclaredSizeReader(body, part.Size)
		if err != nil {
			return SQLiteUploadResult{}, err
		}
		body = bounded
	}
	headers := http.Header{}
	headers.Set("content-type", "application/gzip")
	headers.Set("x-crawl-sqlite-upload", "bundle-part")
	headers.Set("x-crawl-bundle-part-index", fmt.Sprintf("%d", part.Index))
	setHeader(headers, "x-crawl-content-sha256", part.SHA256)
	setHeader(headers, "x-crawl-compression", part.Compression)
	setHeader(headers, "x-crawl-snapshot-id", part.SnapshotID)
	var out SQLiteUploadResult
	err := c.doRaw(ctx, http.MethodPut, archivePath(app, archive, "sqlite"), body, part.Size, headers, &out, true)
	return out, err
}

func (c *Client) UploadSQLiteBundleFiles(ctx context.Context, app, archive string, manifest SQLiteBundleManifest, parts []SQLiteBundlePartFile) (SQLiteBundleUploadResult, error) {
	preparedManifest, manifestBody, err := prepareSQLiteBundleManifest(app, archive, manifest)
	if err != nil {
		return SQLiteBundleUploadResult{}, err
	}
	if preparedManifest.SnapshotID == "" {
		expectedParts, err := validateMutableSQLiteBundleFiles(ctx, preparedManifest, parts)
		if err != nil {
			return SQLiteBundleUploadResult{}, err
		}
		return c.uploadMutableSQLiteBundleFiles(
			ctx,
			app,
			archive,
			preparedManifest,
			manifestBody,
			parts,
			expectedParts,
		)
	}
	validatedParts, err := openValidatedSnapshotSQLiteBundleFiles(ctx, preparedManifest, parts)
	if err != nil {
		return SQLiteBundleUploadResult{}, err
	}
	defer closeValidatedSnapshotSQLiteBundleFiles(validatedParts)
	for _, part := range validatedParts {
		_, uploadErr := c.UploadSQLiteBundlePart(ctx, app, archive, SQLiteBundlePartUpload{
			Index:       part.part.Index,
			Body:        part.file,
			Size:        part.part.Size,
			SHA256:      part.part.SHA256,
			Compression: SQLiteGzipCompression,
			SnapshotID:  preparedManifest.SnapshotID,
		})
		if uploadErr != nil {
			return SQLiteBundleUploadResult{}, uploadErr
		}
	}
	return c.uploadSQLiteBundleManifest(
		ctx,
		app,
		archive,
		preparedManifest.SnapshotID,
		manifestBody,
	)
}

func (c *Client) uploadMutableSQLiteBundleFiles(
	ctx context.Context,
	app, archive string,
	manifest SQLiteBundleManifest,
	manifestBody []byte,
	parts []SQLiteBundlePartFile,
	expectedParts map[int]validatedMutableSQLiteBundlePartFile,
) (SQLiteBundleUploadResult, error) {
	for _, part := range parts {
		validated := expectedParts[part.Index]
		expected := validated.part
		file, err := os.Open(part.Path)
		if err != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("open sqlite bundle part %d: %w", part.Index, err)
		}
		infoBefore, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return SQLiteBundleUploadResult{}, fmt.Errorf("stat sqlite bundle part %d: %w", part.Index, err)
		}
		if !infoBefore.Mode().IsRegular() || infoBefore.Size() != validated.localSize {
			_ = file.Close()
			return SQLiteBundleUploadResult{}, fmt.Errorf(
				"sqlite bundle part file %d must be a %d-byte regular file",
				part.Index,
				validated.localSize,
			)
		}
		bounded, err := sqliteBundleDeclaredSizeReader(file, validated.localSize)
		if err != nil {
			_ = file.Close()
			return SQLiteBundleUploadResult{}, err
		}
		hash := sha256.New()
		var streamed byteCounter
		body := io.TeeReader(bounded, io.MultiWriter(hash, &streamed))
		_, uploadErr := c.UploadSQLiteBundlePart(ctx, app, archive, SQLiteBundlePartUpload{
			Index:       expected.Index,
			Body:        body,
			Size:        expected.Size,
			SHA256:      expected.SHA256,
			Compression: SQLiteGzipCompression,
		})
		var drainErr error
		if uploadErr == nil {
			_, drainErr = copyWithContext(ctx, io.Discard, body)
		}
		infoAfter, statErr := file.Stat()
		closeErr := file.Close()
		if uploadErr != nil {
			return SQLiteBundleUploadResult{}, uploadErr
		}
		if drainErr != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf(
				"verify sqlite bundle part %d upload: %w",
				part.Index,
				drainErr,
			)
		}
		if statErr != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("restat sqlite bundle part %d: %w", part.Index, statErr)
		}
		if closeErr != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("close sqlite bundle part %d: %w", part.Index, closeErr)
		}
		expectedSHA256 := strings.TrimSpace(expected.SHA256)
		digestChanged := expectedSHA256 != "" &&
			!strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), expectedSHA256)
		if !os.SameFile(infoBefore, infoAfter) ||
			infoBefore.Size() != validated.localSize ||
			infoAfter.Size() != validated.localSize ||
			int64(streamed) != validated.localSize ||
			digestChanged {
			return SQLiteBundleUploadResult{}, fmt.Errorf(
				"sqlite bundle part file %d changed during upload",
				part.Index,
			)
		}
	}
	return c.uploadSQLiteBundleManifest(ctx, app, archive, manifest.SnapshotID, manifestBody)
}

type validatedMutableSQLiteBundlePartFile struct {
	part      SQLiteBundlePart
	localSize int64
}

type byteCounter int64

func (counter *byteCounter) Write(p []byte) (int, error) {
	*counter += byteCounter(len(p))
	return len(p), nil
}

func (c *Client) UploadSQLiteBundleManifest(ctx context.Context, app, archive string, manifest SQLiteBundleManifest) (SQLiteBundleUploadResult, error) {
	_, manifestBody, err := prepareSQLiteBundleManifest(app, archive, manifest)
	if err != nil {
		return SQLiteBundleUploadResult{}, err
	}
	return c.uploadSQLiteBundleManifest(ctx, app, archive, manifest.SnapshotID, manifestBody)
}

func (c *Client) uploadSQLiteBundleManifest(
	ctx context.Context,
	app, archive, snapshotID string,
	manifestBody []byte,
) (SQLiteBundleUploadResult, error) {
	headers := http.Header{}
	headers.Set("content-type", "application/json")
	headers.Set("x-crawl-sqlite-upload", "bundle-manifest")
	setHeader(headers, "x-crawl-snapshot-id", snapshotID)
	var out SQLiteBundleUploadResult
	err := c.doRaw(
		ctx,
		http.MethodPut,
		archivePath(app, archive, "sqlite"),
		bytes.NewReader(manifestBody),
		int64(len(manifestBody)),
		headers,
		&out,
		true,
	)
	return out, err
}

func prepareSQLiteBundleManifest(app, archive string, manifest SQLiteBundleManifest) (SQLiteBundleManifest, []byte, error) {
	app = strings.TrimSpace(app)
	archive = strings.TrimSpace(archive)
	if strings.TrimSpace(manifest.App) == "" {
		manifest.App = app
	}
	if strings.TrimSpace(manifest.Archive) == "" {
		manifest.Archive = archive
	}
	snapshotScoped := manifest.SnapshotID != ""
	if snapshotScoped {
		if err := validateSQLiteBundleManifest(manifest, app, archive); err != nil {
			return SQLiteBundleManifest{}, nil, err
		}
	}
	maxBodySize := sqliteBundleManifestSizeLimit(snapshotScoped)
	encodedSize, err := preflightSQLiteBundleManifestEncoding(manifest, maxBodySize)
	if err != nil {
		return SQLiteBundleManifest{}, nil, err
	}
	var buf bytes.Buffer
	buf.Grow(int(encodedSize) + 1)
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return SQLiteBundleManifest{}, nil, fmt.Errorf("encode sqlite bundle manifest: %w", err)
	}
	body := buf.Bytes()
	if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	if int64(len(body)) != encodedSize {
		return SQLiteBundleManifest{}, nil, errors.New(
			"sqlite bundle manifest encoding size changed after preflight",
		)
	}
	if err := validateSQLiteBundleManifestSize(int64(len(body)), snapshotScoped); err != nil {
		return SQLiteBundleManifest{}, nil, err
	}
	return manifest, body, nil
}

func sqliteBundleManifestSizeLimit(snapshotScoped bool) int64 {
	if snapshotScoped {
		return maxSQLiteSnapshotBundleManifestBytes
	}
	return maxSQLiteMutableBundleManifestBytes
}

func validateSQLiteBundleManifestSize(size int64, snapshotScoped bool) error {
	maxBodySize := sqliteBundleManifestSizeLimit(snapshotScoped)
	if size > maxBodySize {
		return fmt.Errorf("sqlite bundle manifest must not exceed %d bytes", maxBodySize)
	}
	return nil
}

const maxSQLiteBundleManifestJSONDepth = 1000

var (
	jsonMarshalerType    = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType    = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	jsonNumberType       = reflect.TypeOf(json.Number(""))
	errManifestSizeLimit = errors.New("sqlite bundle manifest size limit exceeded")
)

type sqliteBundleJSONVisit struct {
	kind reflect.Kind
	typ  reflect.Type
	ptr  uintptr
}

type sqliteBundleJSONSizeCounter struct {
	size  int64
	limit int64
	seen  map[sqliteBundleJSONVisit]struct{}
}

func preflightSQLiteBundleManifestEncoding(
	manifest SQLiteBundleManifest,
	limit int64,
) (int64, error) {
	counter := sqliteBundleJSONSizeCounter{
		limit: limit,
		seen:  make(map[sqliteBundleJSONVisit]struct{}),
	}
	if err := counter.addValue(reflect.ValueOf(manifest), 0); err != nil {
		if errors.Is(err, errManifestSizeLimit) {
			return 0, fmt.Errorf("sqlite bundle manifest must not exceed %d bytes", limit)
		}
		return 0, fmt.Errorf("encode sqlite bundle manifest: %w", err)
	}
	return counter.size, nil
}

func (c *sqliteBundleJSONSizeCounter) add(size int64) error {
	if size < 0 || size > c.limit-c.size {
		return errManifestSizeLimit
	}
	c.size += size
	return nil
}

func (c *sqliteBundleJSONSizeCounter) addIndent(depth int) error {
	return c.add(int64(depth * 2))
}

func (c *sqliteBundleJSONSizeCounter) addString(value string) error {
	if err := c.add(2); err != nil {
		return err
	}
	for len(value) > 0 {
		char, width := utf8.DecodeRuneInString(value)
		value = value[width:]
		size := int64(width)
		switch {
		case char == utf8.RuneError && width == 1:
			size = 6
		case char == '\\' || char == '"':
			size = 2
		case char == '\b' || char == '\f' || char == '\n' || char == '\r' || char == '\t':
			size = 2
		case char < 0x20 || char == '\u2028' || char == '\u2029':
			size = 6
		}
		if err := c.add(size); err != nil {
			return err
		}
	}
	return nil
}

func (c *sqliteBundleJSONSizeCounter) addValue(value reflect.Value, depth int) error {
	if depth > maxSQLiteBundleManifestJSONDepth {
		return &json.UnsupportedValueError{
			Value: value,
			Str:   "exceeded maximum sqlite bundle manifest nesting depth",
		}
	}
	if !value.IsValid() {
		return c.add(4)
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return c.add(4)
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return c.add(4)
	}
	if value.Type() == jsonNumberType {
		number := value.String()
		if !validJSONNumber(number) {
			return fmt.Errorf("json: invalid number literal %q", number)
		}
		return c.add(int64(len(number)))
	}
	if value.Type().Implements(jsonMarshalerType) ||
		value.Type().Implements(textMarshalerType) ||
		(value.Kind() != reflect.Pointer &&
			(reflect.PointerTo(value.Type()).Implements(jsonMarshalerType) ||
				reflect.PointerTo(value.Type()).Implements(textMarshalerType))) {
		return &json.UnsupportedTypeError{Type: value.Type()}
	}

	switch value.Kind() {
	case reflect.Pointer:
		if err := c.enter(value); err != nil {
			return err
		}
		defer c.leave(value)
		return c.addValue(value.Elem(), depth)
	case reflect.Bool:
		if value.Bool() {
			return c.add(4)
		}
		return c.add(5)
	case reflect.String:
		return c.addString(value.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return c.add(int64(len(strconv.FormatInt(value.Int(), 10))))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return c.add(int64(len(strconv.FormatUint(value.Uint(), 10))))
	case reflect.Float32, reflect.Float64:
		number, err := sqliteBundleJSONFloat(value.Float(), value.Type().Bits())
		if err != nil {
			return err
		}
		return c.add(int64(len(number)))
	case reflect.Slice:
		if value.IsNil() {
			return c.add(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			length := value.Len()
			if int64(length) > c.limit-c.size {
				return errManifestSizeLimit
			}
			return c.add(int64(base64.StdEncoding.EncodedLen(length) + 2))
		}
		if err := c.enter(value); err != nil {
			return err
		}
		defer c.leave(value)
		return c.addArray(value, depth)
	case reflect.Array:
		return c.addArray(value, depth)
	case reflect.Map:
		if value.IsNil() {
			return c.add(4)
		}
		if value.Type().Key().Kind() != reflect.String ||
			value.Type().Key().Implements(textMarshalerType) {
			return &json.UnsupportedTypeError{Type: value.Type()}
		}
		if err := c.enter(value); err != nil {
			return err
		}
		defer c.leave(value)
		return c.addMap(value, depth)
	case reflect.Struct:
		return c.addStruct(value, depth)
	default:
		return &json.UnsupportedTypeError{Type: value.Type()}
	}
}

func (c *sqliteBundleJSONSizeCounter) addArray(value reflect.Value, depth int) error {
	if value.Len() == 0 {
		return c.add(2)
	}
	if err := c.add(1); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if index == 0 {
			if err := c.add(1); err != nil {
				return err
			}
		} else if err := c.add(2); err != nil {
			return err
		}
		if err := c.addIndent(depth + 1); err != nil {
			return err
		}
		if err := c.addValue(value.Index(index), depth+1); err != nil {
			return err
		}
	}
	if err := c.add(1); err != nil {
		return err
	}
	if err := c.addIndent(depth); err != nil {
		return err
	}
	return c.add(1)
}

func (c *sqliteBundleJSONSizeCounter) addMap(value reflect.Value, depth int) error {
	if value.Len() == 0 {
		return c.add(2)
	}
	if err := c.add(1); err != nil {
		return err
	}
	iterator := value.MapRange()
	first := true
	for iterator.Next() {
		if first {
			first = false
			if err := c.add(1); err != nil {
				return err
			}
		} else if err := c.add(2); err != nil {
			return err
		}
		if err := c.addIndent(depth + 1); err != nil {
			return err
		}
		if err := c.addString(iterator.Key().String()); err != nil {
			return err
		}
		if err := c.add(2); err != nil {
			return err
		}
		if err := c.addValue(iterator.Value(), depth+1); err != nil {
			return err
		}
	}
	if err := c.add(1); err != nil {
		return err
	}
	if err := c.addIndent(depth); err != nil {
		return err
	}
	return c.add(1)
}

func (c *sqliteBundleJSONSizeCounter) addStruct(value reflect.Value, depth int) error {
	if err := c.add(1); err != nil {
		return err
	}
	first := true
	typ := value.Type()
	for index := 0; index < value.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath != "" {
			continue
		}
		if field.Anonymous {
			return &json.UnsupportedTypeError{Type: typ}
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		for _, option := range strings.Split(options, ",") {
			if option != "" && option != "omitempty" {
				return &json.UnsupportedTypeError{Type: typ}
			}
		}
		if name == "" {
			name = field.Name
		}
		fieldValue := value.Field(index)
		if strings.Contains(","+options+",", ",omitempty,") && isEmptyJSONValue(fieldValue) {
			continue
		}
		if first {
			first = false
			if err := c.add(1); err != nil {
				return err
			}
		} else if err := c.add(2); err != nil {
			return err
		}
		if err := c.addIndent(depth + 1); err != nil {
			return err
		}
		if err := c.addString(name); err != nil {
			return err
		}
		if err := c.add(2); err != nil {
			return err
		}
		if err := c.addValue(fieldValue, depth+1); err != nil {
			return err
		}
	}
	if first {
		return c.add(1)
	}
	if err := c.add(1); err != nil {
		return err
	}
	if err := c.addIndent(depth); err != nil {
		return err
	}
	return c.add(1)
}

func (c *sqliteBundleJSONSizeCounter) enter(value reflect.Value) error {
	visit := sqliteBundleJSONVisit{
		kind: value.Kind(),
		typ:  value.Type(),
		ptr:  value.Pointer(),
	}
	if _, exists := c.seen[visit]; exists {
		return &json.UnsupportedValueError{
			Value: value,
			Str:   "encountered a cycle in sqlite bundle manifest",
		}
	}
	c.seen[visit] = struct{}{}
	return nil
}

func (c *sqliteBundleJSONSizeCounter) leave(value reflect.Value) {
	delete(c.seen, sqliteBundleJSONVisit{
		kind: value.Kind(),
		typ:  value.Type(),
		ptr:  value.Pointer(),
	})
}

func isEmptyJSONValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Interface, reflect.Pointer:
		return value.IsZero()
	default:
		return false
	}
}

func sqliteBundleJSONFloat(value float64, bits int) (string, error) {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return "", &json.UnsupportedValueError{
			Value: reflect.ValueOf(value),
			Str:   strconv.FormatFloat(value, 'g', -1, bits),
		}
	}
	format := byte('f')
	absolute := math.Abs(value)
	if absolute != 0 && (absolute < 1e-6 || absolute >= 1e21) {
		format = 'e'
	}
	encoded := strconv.AppendFloat(nil, value, format, -1, bits)
	if format == 'e' {
		for index := 0; index+3 < len(encoded); index++ {
			if encoded[index] == 'e' &&
				(encoded[index+1] == '-' || encoded[index+1] == '+') &&
				encoded[index+2] == '0' {
				encoded = append(encoded[:index+2], encoded[index+3:]...)
				break
			}
		}
	}
	return string(encoded), nil
}

func validJSONNumber(value string) bool {
	if value == "" {
		return false
	}
	index := 0
	if value[index] == '-' {
		index++
		if index == len(value) {
			return false
		}
	}
	if value[index] == '0' {
		index++
	} else {
		if value[index] < '1' || value[index] > '9' {
			return false
		}
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
	}
	if index < len(value) && value[index] == '.' {
		index++
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		index++
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			index++
		}
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == start {
			return false
		}
	}
	return index == len(value)
}

func validateSQLiteBundleManifest(manifest SQLiteBundleManifest, app, archive string) error {
	snapshotScoped := manifest.SnapshotID != ""
	if snapshotScoped && !validSQLiteSnapshotID(manifest.SnapshotID) {
		return errors.New("sqlite bundle snapshot id must be empty or a lowercase sha256 digest")
	}
	if manifest.Format != SQLiteGzipChunkedBundleFormat {
		return fmt.Errorf("sqlite bundle format must be %q", SQLiteGzipChunkedBundleFormat)
	}
	if app == "" || archive == "" || manifest.App != app || manifest.Archive != archive {
		return errors.New("sqlite bundle manifest app and archive must match the upload route")
	}
	if manifest.ContentType != "" && manifest.ContentType != "application/vnd.sqlite3" {
		return errors.New("sqlite bundle content type must be application/vnd.sqlite3 when set")
	}
	if snapshotScoped && manifest.GeneratedAt != "" {
		return errors.New("snapshot sqlite bundle generated_at must be omitted")
	}
	if manifest.GeneratedAt != "" {
		if err := validateSQLiteBundleMetadata(manifest.GeneratedAt, "sqlite bundle generated_at"); err != nil {
			return err
		}
	}
	if manifest.Compression.Algorithm != SQLiteGzipCompression {
		return fmt.Errorf("sqlite bundle compression must be %q", SQLiteGzipCompression)
	}
	if snapshotScoped {
		if manifest.Reconstruct != "" && manifest.Reconstruct != snapshotSQLiteReconstructSteps {
			return fmt.Errorf("sqlite bundle reconstruct must be %q", snapshotSQLiteReconstructSteps)
		}
		for name := range manifest.Privacy {
			if err := validateSQLiteBundleMapKey(name, "sqlite bundle privacy key"); err != nil {
				return err
			}
		}
	}
	if err := validateSQLiteBundleManifestLimits(manifest); err != nil {
		return err
	}
	if manifest.Object.Key != SQLiteSnapshotObjectKey(app, archive, manifest.SnapshotID) {
		return errors.New("sqlite bundle object key must match the upload route")
	}
	if !validSQLiteBundleSHA256(manifest.Object.SHA256, snapshotScoped) {
		return errors.New("sqlite bundle object sha256 must be a valid digest")
	}
	if snapshotScoped && manifest.Object.SHA256 != manifest.SnapshotID {
		return errors.New("sqlite bundle snapshot id must equal the object sha256")
	}
	if manifest.CompressedObject.Key != SQLiteSnapshotCompressedObjectKey(
		app,
		archive,
		manifest.SnapshotID,
		manifest.CompressedObject.SHA256,
	) {
		return errors.New("sqlite bundle compressed object key must match the upload route")
	}
	if !validSQLiteBundleSHA256(manifest.CompressedObject.SHA256, snapshotScoped) {
		return errors.New("sqlite bundle compressed object sha256 must be a valid digest")
	}
	for index, part := range manifest.Parts {
		if !validSQLiteBundleSHA256(part.SHA256, snapshotScoped) {
			return fmt.Errorf("sqlite bundle part %d sha256 must be a valid digest", index)
		}
		if part.Key != SQLiteSnapshotBundlePartKey(
			app,
			archive,
			manifest.SnapshotID,
			part.SHA256,
			index,
		) {
			return fmt.Errorf("sqlite bundle part %d key must match the upload route", index)
		}
	}
	for name, count := range manifest.Counts {
		if name == "" {
			return errors.New("sqlite bundle count names must not be empty")
		}
		if count < 0 {
			return fmt.Errorf("sqlite bundle count %q must not be negative", name)
		}
		if snapshotScoped {
			if err := validateSQLiteBundleMapKey(name, "sqlite bundle count name"); err != nil {
				return err
			}
			if count > maxSQLiteBundleSafeInteger {
				return fmt.Errorf(
					"sqlite bundle count %q must be a non-negative safe integer",
					name,
				)
			}
		}
	}
	return nil
}

func validateSQLiteBundleMapKey(value, label string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	return validateSQLiteBundleMetadata(value, label)
}

func validateSQLiteBundleMetadata(value, label string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if len(value) > maxSQLiteBundleMetadataBytes {
		return fmt.Errorf("%s must not exceed %d UTF-8 bytes", label, maxSQLiteBundleMetadataBytes)
	}
	return nil
}

func validSQLiteBundleSHA256(value string, canonical bool) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range value {
		if char >= '0' && char <= '9' {
			continue
		}
		if char >= 'a' && char <= 'f' {
			continue
		}
		if !canonical && char >= 'A' && char <= 'F' {
			continue
		}
		return false
	}
	return true
}

func validateSQLiteBundlePartLimit(index int, size int64, snapshotScoped bool) error {
	if index < 0 {
		return fmt.Errorf("sqlite bundle part index must not be negative")
	}
	if snapshotScoped && index >= maxSQLiteBundleParts {
		return fmt.Errorf("sqlite bundle part index must be between 0 and %d", maxSQLiteBundleParts-1)
	}
	if snapshotScoped && size < 0 {
		return fmt.Errorf("sqlite bundle part %d size must be non-negative", index)
	}
	if !snapshotScoped && size < -1 {
		return fmt.Errorf("sqlite bundle part %d size must be -1 or non-negative", index)
	}
	if snapshotScoped && size > DefaultSQLiteBundleChunkSize {
		return fmt.Errorf(
			"sqlite bundle part %d size must be between 0 and %d bytes",
			index,
			DefaultSQLiteBundleChunkSize,
		)
	}
	return nil
}

func validateSQLiteBundleManifestLimits(manifest SQLiteBundleManifest) error {
	snapshotScoped := manifest.SnapshotID != ""
	if len(manifest.Parts) == 0 {
		return fmt.Errorf("sqlite bundle manifest must contain at least one part")
	}
	if snapshotScoped && len(manifest.Parts) > maxSQLiteBundleParts {
		return fmt.Errorf("sqlite bundle manifest must contain between 1 and %d parts", maxSQLiteBundleParts)
	}
	if manifest.Object.Size <= 0 {
		return fmt.Errorf("sqlite bundle object size must be positive")
	}
	if snapshotScoped && manifest.Object.Size > maxSQLiteBundleObjectSize {
		return fmt.Errorf("sqlite bundle object size must be between 1 and %d bytes", maxSQLiteBundleObjectSize)
	}
	if manifest.CompressedObject.Size <= 0 {
		return fmt.Errorf("sqlite bundle compressed size must be positive")
	}
	if snapshotScoped && manifest.CompressedObject.Size > maxSQLiteBundleCompressedSize {
		return fmt.Errorf("sqlite bundle compressed size must be between 1 and %d bytes", maxSQLiteBundleCompressedSize)
	}
	var total int64
	for index, part := range manifest.Parts {
		if part.Index != index {
			return fmt.Errorf("sqlite bundle manifest part %d has index %d", index, part.Index)
		}
		if part.Size <= 0 {
			return fmt.Errorf("sqlite bundle part %d size must be positive", part.Index)
		}
		if err := validateSQLiteBundlePartLimit(part.Index, part.Size, snapshotScoped); err != nil {
			return err
		}
		if snapshotScoped && total > maxSQLiteBundleCompressedSize-part.Size {
			return fmt.Errorf("sqlite bundle parts exceed %d compressed bytes", maxSQLiteBundleCompressedSize)
		}
		if total > manifest.CompressedObject.Size ||
			part.Size > manifest.CompressedObject.Size-total {
			return fmt.Errorf("sqlite bundle part sizes exceed the declared compressed size")
		}
		total += part.Size
	}
	if total != manifest.CompressedObject.Size {
		return fmt.Errorf(
			"sqlite bundle part sizes total %d bytes, want compressed size %d",
			total,
			manifest.CompressedObject.Size,
		)
	}
	return nil
}

type validatedSnapshotSQLiteBundlePartFile struct {
	part    SQLiteBundlePart
	file    *os.File
	tempDir string
}

func openValidatedSnapshotSQLiteBundleFiles(
	ctx context.Context,
	manifest SQLiteBundleManifest,
	parts []SQLiteBundlePartFile,
) (_ []validatedSnapshotSQLiteBundlePartFile, err error) {
	if err := validateSQLiteBundleManifest(manifest, manifest.App, manifest.Archive); err != nil {
		return nil, err
	}
	if manifest.SnapshotID == "" {
		return nil, fmt.Errorf("sqlite bundle snapshot id is required for immutable upload staging")
	}
	if len(parts) != len(manifest.Parts) {
		return nil, fmt.Errorf("sqlite bundle has %d part files, want %d", len(parts), len(manifest.Parts))
	}
	tempDir, err := os.MkdirTemp("", "crawl-sqlite-upload-*")
	if err != nil {
		return nil, fmt.Errorf("create sqlite bundle upload snapshot: %w", err)
	}
	validated := make([]validatedSnapshotSQLiteBundlePartFile, 0, len(parts))
	defer func() {
		if err != nil {
			closeValidatedSnapshotSQLiteBundleFiles(validated)
			_ = os.RemoveAll(tempDir)
		}
	}()
	for index, part := range parts {
		expected := manifest.Parts[index]
		if part.SQLiteBundlePart != expected {
			return nil, fmt.Errorf("sqlite bundle part file %d does not match the manifest", index)
		}
		snapshot, err := snapshotSQLiteBundlePart(ctx, tempDir, index, part)
		if err != nil {
			return nil, err
		}
		validated = append(validated, validatedSnapshotSQLiteBundlePartFile{
			part:    expected,
			file:    snapshot,
			tempDir: tempDir,
		})
	}
	if err := validateSnapshotSQLiteBundleContent(ctx, manifest, validated); err != nil {
		return nil, err
	}
	return validated, nil
}

func validateMutableSQLiteBundleFiles(
	ctx context.Context,
	manifest SQLiteBundleManifest,
	parts []SQLiteBundlePartFile,
) (map[int]validatedMutableSQLiteBundlePartFile, error) {
	if len(manifest.Parts) > 0 && len(parts) != len(manifest.Parts) {
		return nil, fmt.Errorf(
			"sqlite bundle has %d part files, want %d",
			len(parts),
			len(manifest.Parts),
		)
	}
	manifestParts := make(map[int]SQLiteBundlePart, len(manifest.Parts))
	for _, part := range manifest.Parts {
		if _, duplicate := manifestParts[part.Index]; duplicate {
			return nil, fmt.Errorf("sqlite bundle manifest repeats part index %d", part.Index)
		}
		manifestParts[part.Index] = part
	}
	expectedParts := make(map[int]validatedMutableSQLiteBundlePartFile, len(parts))
	for _, part := range parts {
		if _, duplicate := expectedParts[part.Index]; duplicate {
			return nil, fmt.Errorf("sqlite bundle part files repeat index %d", part.Index)
		}
		expected := part.SQLiteBundlePart
		if len(manifestParts) > 0 {
			var ok bool
			expected, ok = manifestParts[part.Index]
			if !ok || part.SQLiteBundlePart != expected {
				return nil, fmt.Errorf(
					"sqlite bundle part file %d does not match the manifest",
					part.Index,
				)
			}
		}
		size, err := validateMutableSQLiteBundlePartFile(ctx, part.Index, part)
		if err != nil {
			return nil, err
		}
		expectedParts[part.Index] = validatedMutableSQLiteBundlePartFile{
			part:      expected,
			localSize: size,
		}
	}
	return expectedParts, nil
}

func validateMutableSQLiteBundlePartFile(
	ctx context.Context,
	index int,
	part SQLiteBundlePartFile,
) (int64, error) {
	if err := validateSQLiteBundlePartLimit(part.Index, part.Size, false); err != nil {
		return 0, err
	}
	source, err := os.Open(part.Path)
	if err != nil {
		return 0, fmt.Errorf("open sqlite bundle part %d: %w", index, err)
	}
	defer func() { _ = source.Close() }()
	infoBefore, err := source.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat sqlite bundle part %d: %w", index, err)
	}
	expectedSize := part.Size
	if expectedSize == -1 {
		expectedSize = infoBefore.Size()
	}
	if !infoBefore.Mode().IsRegular() || infoBefore.Size() != expectedSize {
		return 0, fmt.Errorf(
			"sqlite bundle part file %d must be a %d-byte regular file",
			index,
			expectedSize,
		)
	}
	hash := sha256.New()
	size, err := copySQLiteBundleDeclaredSize(ctx, hash, source, expectedSize)
	if err != nil {
		return 0, fmt.Errorf("validate sqlite bundle part %d: %w", index, err)
	}
	infoAfter, err := source.Stat()
	if err != nil {
		return 0, fmt.Errorf("restat sqlite bundle part %d: %w", index, err)
	}
	if !os.SameFile(infoBefore, infoAfter) || infoAfter.Size() != expectedSize || size != expectedSize {
		return 0, fmt.Errorf("sqlite bundle part file %d changed during validation", index)
	}
	expectedSHA256 := strings.TrimSpace(part.SHA256)
	if expectedSHA256 != "" &&
		!strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), expectedSHA256) {
		return 0, fmt.Errorf("sqlite bundle part file %d sha256 does not match the manifest", index)
	}
	return expectedSize, nil
}

func snapshotSQLiteBundlePart(
	ctx context.Context,
	tempDir string,
	index int,
	part SQLiteBundlePartFile,
) (_ *os.File, err error) {
	snapshotPath := filepath.Join(tempDir, fmt.Sprintf("part-%04d", index))
	snapshot, err := os.OpenFile(snapshotPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create sqlite bundle part snapshot %d: %w", index, err)
	}
	defer func() {
		if err != nil {
			_ = snapshot.Close()
		}
	}()
	if err := copyValidatedSQLiteBundlePart(ctx, index, part, snapshot); err != nil {
		return nil, err
	}
	if err := snapshot.Sync(); err != nil {
		return nil, fmt.Errorf("sync sqlite bundle part snapshot %d: %w", index, err)
	}
	if err := snapshot.Close(); err != nil {
		return nil, fmt.Errorf("close sqlite bundle part snapshot %d: %w", index, err)
	}
	snapshot, err = os.Open(snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("reopen sqlite bundle part snapshot %d: %w", index, err)
	}
	return snapshot, nil
}

func copyValidatedSQLiteBundlePart(
	ctx context.Context,
	index int,
	part SQLiteBundlePartFile,
	dst io.Writer,
) error {
	source, err := os.Open(part.Path)
	if err != nil {
		return fmt.Errorf("open sqlite bundle part %d: %w", index, err)
	}
	defer func() { _ = source.Close() }()
	infoBefore, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat sqlite bundle part %d: %w", index, err)
	}
	if !infoBefore.Mode().IsRegular() || infoBefore.Size() != part.Size {
		return fmt.Errorf("sqlite bundle part file %d must be a %d-byte regular file", index, part.Size)
	}
	hash := sha256.New()
	size, err := copySQLiteBundleDeclaredSize(
		ctx,
		io.MultiWriter(dst, hash),
		source,
		part.Size,
	)
	if err != nil {
		return fmt.Errorf("validate sqlite bundle part %d: %w", index, err)
	}
	infoAfter, err := source.Stat()
	if err != nil {
		return fmt.Errorf("restat sqlite bundle part %d: %w", index, err)
	}
	if !os.SameFile(infoBefore, infoAfter) || infoAfter.Size() != part.Size || size != part.Size {
		return fmt.Errorf("sqlite bundle part file %d changed during validation", index)
	}
	actualSHA256 := fmt.Sprintf("%x", hash.Sum(nil))
	if !strings.EqualFold(actualSHA256, part.SHA256) {
		return fmt.Errorf("sqlite bundle part file %d sha256 does not match the manifest", index)
	}
	return nil
}

func validateSnapshotSQLiteBundleContent(
	ctx context.Context,
	manifest SQLiteBundleManifest,
	parts []validatedSnapshotSQLiteBundlePartFile,
) error {
	compressedHash := sha256.New()
	var compressedSize int64
	for index, part := range parts {
		size, err := copySQLiteBundleDeclaredSize(
			ctx,
			compressedHash,
			part.file,
			part.part.Size,
		)
		if err != nil {
			return fmt.Errorf("hash sqlite bundle compressed part %d: %w", index, err)
		}
		if size != part.part.Size {
			return fmt.Errorf("sqlite bundle compressed object does not match the manifest")
		}
		compressedSize += size
	}
	if compressedSize != manifest.CompressedObject.Size ||
		fmt.Sprintf("%x", compressedHash.Sum(nil)) != manifest.CompressedObject.SHA256 {
		return fmt.Errorf("sqlite bundle compressed object does not match the manifest")
	}
	if err := rewindValidatedSQLiteBundleFiles(parts); err != nil {
		return err
	}
	readers := make([]io.Reader, len(parts))
	for index := range parts {
		reader, err := sqliteBundleDeclaredSizeReader(
			parts[index].file,
			parts[index].part.Size,
		)
		if err != nil {
			return err
		}
		readers[index] = reader
	}
	decompressor, err := gzip.NewReader(io.MultiReader(readers...))
	if err != nil {
		return fmt.Errorf("decompress sqlite bundle snapshot: %w", err)
	}
	objectHash := sha256.New()
	objectSize, copyErr := copyWithContext(
		ctx,
		objectHash,
		io.LimitReader(decompressor, manifest.Object.Size+1),
	)
	closeErr := decompressor.Close()
	if copyErr != nil {
		return fmt.Errorf("decompress sqlite bundle snapshot: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sqlite bundle decompressor: %w", closeErr)
	}
	if objectSize != manifest.Object.Size ||
		fmt.Sprintf("%x", objectHash.Sum(nil)) != manifest.Object.SHA256 {
		return fmt.Errorf("sqlite bundle decompressed object does not match the manifest")
	}
	return rewindValidatedSQLiteBundleFiles(parts)
}

func rewindValidatedSQLiteBundleFiles(parts []validatedSnapshotSQLiteBundlePartFile) error {
	for index, part := range parts {
		if _, err := part.file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind sqlite bundle part %d: %w", index, err)
		}
	}
	return nil
}

func closeValidatedSnapshotSQLiteBundleFiles(parts []validatedSnapshotSQLiteBundlePartFile) {
	tempDirs := map[string]struct{}{}
	for _, part := range parts {
		_ = part.file.Close()
		if part.tempDir != "" {
			tempDirs[part.tempDir] = struct{}{}
		}
	}
	for tempDir := range tempDirs {
		_ = os.RemoveAll(tempDir)
	}
}

func (c *Client) StartGitHubLogin(ctx context.Context, pollSecretHash string) (LoginStartResult, error) {
	var out LoginStartResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/start", LoginStartRequest{PollSecretHash: pollSecretHash}, &out, false)
	return out, err
}

func (c *Client) PollGitHubLogin(ctx context.Context, loginID, pollSecret string) (LoginPollResult, error) {
	var out LoginPollResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/poll", LoginPollRequest{LoginID: loginID, PollSecret: pollSecret}, &out, false)
	return out, err
}

func (c *Client) LoginWithGitHubToken(ctx context.Context, token string) (LoginPollResult, error) {
	var out LoginPollResult
	err := c.do(ctx, http.MethodPost, "/v1/auth/github/token", GitHubTokenLoginRequest{Token: strings.TrimSpace(token)}, &out, false)
	return out, err
}

type Error struct {
	Status  int    `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	code := strings.TrimSpace(e.Code)
	if code == "" {
		return fmt.Sprintf("remote request failed: status=%d message=%s", e.Status, msg)
	}
	return fmt.Sprintf("remote request failed: status=%d code=%s message=%s", e.Status, code, msg)
}

func (c *Client) do(ctx context.Context, method, route string, input, output any, auth bool) error {
	var body io.Reader
	if input != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(input); err != nil {
			return fmt.Errorf("encode remote request: %w", err)
		}
		body = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(route), body)
	if err != nil {
		return err
	}
	return c.doRequest(ctx, req, input != nil, output, auth)
}

func (c *Client) doRaw(ctx context.Context, method, route string, body io.Reader, size int64, headers http.Header, output any, auth bool) error {
	req, err := http.NewRequestWithContext(ctx, method, c.url(route), body)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	} else {
		req.ContentLength = -1
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return c.doRequest(ctx, req, true, output, auth)
}

func (c *Client) doRequest(ctx context.Context, req *http.Request, hasBody bool, output any, auth bool) error {
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", c.userAgent)
	if hasBody && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if auth {
		if c.tokenProvider == nil {
			return ErrMissingToken
		}
		token, err := c.tokenProvider.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeRemoteError(resp)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		return fmt.Errorf("decode remote response: %w", err)
	}
	return nil
}

func setHeader(headers http.Header, name, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		headers.Set(name, value)
	}
}

func intHeader(value int) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", value)
}

func (c *Client) url(route string) string {
	route = "/" + strings.TrimLeft(route, "/")
	u := *c.endpoint
	escapedPath := strings.TrimRight(u.EscapedPath(), "/") + route
	unescapedPath, err := url.PathUnescape(escapedPath)
	if err == nil {
		u.Path = unescapedPath
		if unescapedPath != escapedPath {
			u.RawPath = escapedPath
		}
	} else {
		u.Path = escapedPath
	}
	return u.String()
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

func decodeRemoteError(resp *http.Response) error {
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	errOut := Error{Status: resp.StatusCode}
	var decoded struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &decoded); err == nil {
		errOut.Code = firstNonEmpty(decoded.Code, decoded.Error)
		errOut.Message = decoded.Message
		if errOut.Message == "" && decoded.Error != "" && decoded.Code == "" {
			errOut.Message = decoded.Error
		}
	}
	if errOut.Message == "" {
		errOut.Message = strings.TrimSpace(string(payload))
	}
	return &errOut
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
