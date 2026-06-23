package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/control"
)

const (
	ModeLocal     = "local"
	ModeGit       = "git"
	ModeCloud     = "cloud"
	ModeHybrid    = "hybrid"
	ModePublisher = "publisher"

	DefaultTokenEnv = "CRAWL_REMOTE_TOKEN"
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

type Archive struct {
	ID            string   `json:"id"`
	App           string   `json:"app"`
	Slug          string   `json:"slug"`
	SchemaName    string   `json:"schema_name,omitempty"`
	SchemaVersion int      `json:"schema_version,omitempty"`
	SchemaHash    string   `json:"schema_hash,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	LastIngestAt  string   `json:"last_ingest_at,omitempty"`
	LastSyncAt    string   `json:"last_sync_at,omitempty"`
}

type Status struct {
	App          string          `json:"app"`
	Archive      string          `json:"archive"`
	Mode         string          `json:"mode,omitempty"`
	GeneratedAt  string          `json:"generated_at,omitempty"`
	LastSyncAt   string          `json:"last_sync_at,omitempty"`
	LastIngestAt string          `json:"last_ingest_at,omitempty"`
	Counts       []control.Count `json:"counts,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	SQLiteObject *SQLiteObject   `json:"sqlite_object,omitempty"`
	SQLiteBundle *SQLiteBundle   `json:"sqlite_bundle,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`
}

type QueryRequest struct {
	App     string         `json:"app,omitempty"`
	Archive string         `json:"archive,omitempty"`
	Name    string         `json:"name"`
	Args    map[string]any `json:"args,omitempty"`
	Limit   int            `json:"limit,omitempty"`
	Cursor  string         `json:"cursor,omitempty"`
}

type QueryStats struct {
	RowsRead    int64  `json:"rows_read,omitempty"`
	RowsWritten int64  `json:"rows_written,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	ServedBy    string `json:"served_by,omitempty"`
}

type QueryResult struct {
	Columns    []string         `json:"columns"`
	Rows       [][]any          `json:"rows"`
	Values     []map[string]any `json:"values,omitempty"`
	Cursor     string           `json:"cursor,omitempty"`
	Stats      QueryStats       `json:"stats,omitempty"`
	SchemaHash string           `json:"schema_hash,omitempty"`
}

type IngestManifest struct {
	App           string `json:"app"`
	Archive       string `json:"archive"`
	SchemaName    string `json:"schema_name,omitempty"`
	SchemaVersion int    `json:"schema_version"`
	SchemaHash    string `json:"schema_hash"`
	Mode          string `json:"mode,omitempty"`
	Source        string `json:"source,omitempty"`
	SourceSyncAt  string `json:"source_sync_at,omitempty"`
}

type IngestRequest struct {
	Manifest IngestManifest `json:"manifest"`
	Table    string         `json:"table"`
	Columns  []string       `json:"columns"`
	Rows     [][]any        `json:"rows"`
	Cursor   string         `json:"cursor,omitempty"`
	Final    bool           `json:"final,omitempty"`
}

type IngestResult struct {
	RunID           string `json:"run_id,omitempty"`
	Table           string `json:"table,omitempty"`
	RowsAccepted    int64  `json:"rows_accepted,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	Complete        bool   `json:"complete,omitempty"`
	ResetIncomplete bool   `json:"reset_incomplete,omitempty"`
	ResetDeleted    int64  `json:"reset_deleted,omitempty"`
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
	headers := http.Header{}
	headers.Set("content-type", "application/gzip")
	headers.Set("x-crawl-sqlite-upload", "bundle-part")
	headers.Set("x-crawl-bundle-part-index", fmt.Sprintf("%d", part.Index))
	setHeader(headers, "x-crawl-content-sha256", part.SHA256)
	setHeader(headers, "x-crawl-compression", part.Compression)
	var out SQLiteUploadResult
	err := c.doRaw(ctx, http.MethodPut, archivePath(app, archive, "sqlite"), part.Body, part.Size, headers, &out, true)
	return out, err
}

func (c *Client) UploadSQLiteBundleFiles(ctx context.Context, app, archive string, manifest SQLiteBundleManifest, parts []SQLiteBundlePartFile) (SQLiteBundleUploadResult, error) {
	for _, part := range parts {
		file, err := os.Open(part.Path)
		if err != nil {
			return SQLiteBundleUploadResult{}, fmt.Errorf("open sqlite bundle part %d: %w", part.Index, err)
		}
		_, uploadErr := c.UploadSQLiteBundlePart(ctx, app, archive, SQLiteBundlePartUpload{
			Index:       part.Index,
			Body:        file,
			Size:        part.Size,
			SHA256:      part.SHA256,
			Compression: SQLiteGzipCompression,
		})
		_ = file.Close()
		if uploadErr != nil {
			return SQLiteBundleUploadResult{}, uploadErr
		}
	}
	return c.UploadSQLiteBundleManifest(ctx, app, archive, manifest)
}

func (c *Client) UploadSQLiteBundleManifest(ctx context.Context, app, archive string, manifest SQLiteBundleManifest) (SQLiteBundleUploadResult, error) {
	if strings.TrimSpace(manifest.App) == "" {
		manifest.App = strings.TrimSpace(app)
	}
	if strings.TrimSpace(manifest.Archive) == "" {
		manifest.Archive = strings.TrimSpace(archive)
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(manifest); err != nil {
		return SQLiteBundleUploadResult{}, fmt.Errorf("encode sqlite bundle manifest: %w", err)
	}
	headers := http.Header{}
	headers.Set("content-type", "application/json")
	headers.Set("x-crawl-sqlite-upload", "bundle-manifest")
	var out SQLiteBundleUploadResult
	err := c.doRaw(ctx, http.MethodPut, archivePath(app, archive, "sqlite"), &buf, int64(buf.Len()), headers, &out, true)
	return out, err
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
