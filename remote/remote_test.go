package remote

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigNormalizeAndEnabled(t *testing.T) {
	cfg := Config{Mode: " Cloud ", Endpoint: "https://remote.test/", Archive: " gitcrawl/openclaw ", Auth: AuthConfig{TokenSource: " Env "}}
	cfg.Normalize()
	if cfg.Mode != ModeCloud || cfg.Endpoint != "https://remote.test" || cfg.Archive != "gitcrawl/openclaw" {
		t.Fatalf("normalized config = %#v", cfg)
	}
	if cfg.TokenEnv != DefaultTokenEnv {
		t.Fatalf("token env = %q", cfg.TokenEnv)
	}
	if !cfg.Enabled() {
		t.Fatal("cloud mode should be enabled")
	}
	cfg.Mode = ModeGit
	if cfg.Enabled() {
		t.Fatal("git mode should not be cloud-enabled")
	}
}

func TestEnvTokenProvider(t *testing.T) {
	t.Setenv("CRAWL_REMOTE_TEST_TOKEN", " tok ")
	token, err := EnvTokenProvider{Name: "CRAWL_REMOTE_TEST_TOKEN"}.Token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token != "tok" {
		t.Fatalf("token = %q", token)
	}
	_, err = EnvTokenProvider{Name: "CRAWL_REMOTE_MISSING_TOKEN"}.Token(context.Background())
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("missing token err = %v", err)
	}
}

func TestClientQuerySendsBearerAndEscapedArchive(t *testing.T) {
	var sawAuth string
	var sawPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("authorization")
		sawPath = r.URL.EscapedPath()
		var req QueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.App != "gitcrawl" || req.Archive != "gitcrawl/openclaw__openclaw" || req.Name != "gitcrawl.threads.search" {
			t.Fatalf("request = %#v", req)
		}
		_ = json.NewEncoder(w).Encode(QueryResult{
			Columns: []string{"number", "title"},
			Rows:    [][]any{{float64(1), "remote"}},
		})
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret"), UserAgent: "test-agent"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	result, err := client.Query(context.Background(), "gitcrawl", "gitcrawl/openclaw__openclaw", QueryRequest{Name: "gitcrawl.threads.search"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if sawAuth != "Bearer secret" {
		t.Fatalf("auth = %q", sawAuth)
	}
	if !strings.Contains(sawPath, "gitcrawl%2Fopenclaw__openclaw") {
		t.Fatalf("path did not escape archive slash: %q", sawPath)
	}
	if len(result.Rows) != 1 || result.Columns[0] != "number" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientUploadSQLiteSendsRawBodyAndMetadata(t *testing.T) {
	var sawAuth string
	var sawPath string
	var sawContentType string
	var sawLength int64
	var sawSHA string
	var sawSchema string
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("authorization")
		sawPath = r.URL.EscapedPath()
		sawContentType = r.Header.Get("content-type")
		sawLength = r.ContentLength
		sawSHA = r.Header.Get("x-crawl-content-sha256")
		sawSchema = r.Header.Get("x-crawl-schema-name")
		bytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		body = string(bytes)
		_ = json.NewEncoder(w).Encode(SQLiteUploadResult{
			App:      "gitcrawl",
			Archive:  "gitcrawl/openclaw__openclaw",
			Complete: true,
			Object:   &SQLiteObject{Key: "gitcrawl/gitcrawl%2Fopenclaw__openclaw/sqlite/current.db", Size: int64(len(bytes)), SHA256: sawSHA},
		})
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret"), UserAgent: "test-agent"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	result, err := client.UploadSQLite(context.Background(), "gitcrawl", "gitcrawl/openclaw__openclaw", SQLiteUploadRequest{
		Body:          strings.NewReader("SQLite bytes"),
		Size:          int64(len("SQLite bytes")),
		ContentSHA256: "abc123",
		SchemaName:    "gitcrawl-cloud-v1",
		SchemaVersion: 1,
		SchemaHash:    "gitcrawl-cloud-v1",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if sawAuth != "Bearer secret" {
		t.Fatalf("auth = %q", sawAuth)
	}
	if !strings.Contains(sawPath, "gitcrawl%2Fopenclaw__openclaw") || !strings.HasSuffix(sawPath, "/sqlite") {
		t.Fatalf("path = %q", sawPath)
	}
	if sawContentType != "application/vnd.sqlite3" {
		t.Fatalf("content-type = %q", sawContentType)
	}
	if sawLength != int64(len("SQLite bytes")) || body != "SQLite bytes" {
		t.Fatalf("body len/body = %d/%q", sawLength, body)
	}
	if sawSHA != "abc123" || sawSchema != "gitcrawl-cloud-v1" {
		t.Fatalf("metadata sha/schema = %q/%q", sawSHA, sawSchema)
	}
	if result.Object == nil || result.Object.Size != int64(len("SQLite bytes")) {
		t.Fatalf("result = %#v", result)
	}
}

func TestBuildGzipSQLiteBundleSplitsAndDescribesParts(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "archive.db")
	payload := strings.Repeat("SQLite format 3\n", 100)
	if err := os.WriteFile(source, []byte(payload), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	bundle, err := BuildGzipSQLiteBundle(context.Background(), SQLiteBundleBuildOptions{
		App:        "gitcrawl",
		Archive:    "gitcrawl/openclaw__openclaw",
		SourcePath: source,
		WorkDir:    dir,
		ChunkSize:  64,
		Counts:     map[string]int64{"threads": 3},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	defer bundle.Cleanup()
	if bundle.Manifest.Format != SQLiteGzipChunkedBundleFormat {
		t.Fatalf("format = %q", bundle.Manifest.Format)
	}
	if bundle.Manifest.Compression.Algorithm != SQLiteGzipCompression {
		t.Fatalf("compression = %#v", bundle.Manifest.Compression)
	}
	if bundle.Manifest.Object.Size != int64(len(payload)) || bundle.Manifest.Object.SHA256 == "" {
		t.Fatalf("object = %#v", bundle.Manifest.Object)
	}
	if bundle.Manifest.CompressedObject.Size <= 0 || bundle.Manifest.CompressedObject.SHA256 == "" {
		t.Fatalf("compressed object = %#v", bundle.Manifest.CompressedObject)
	}
	if len(bundle.Parts) < 1 || len(bundle.Manifest.Parts) != len(bundle.Parts) {
		t.Fatalf("parts = %#v manifest=%#v", bundle.Parts, bundle.Manifest.Parts)
	}
	var compressed strings.Builder
	for _, part := range bundle.Parts {
		bytes, err := os.ReadFile(part.Path)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		compressed.Write(bytes)
		if !strings.Contains(part.Key, "current.db.gz.part-") {
			t.Fatalf("part key = %q", part.Key)
		}
	}
	reader, err := gzip.NewReader(strings.NewReader(compressed.String()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if string(decompressed) != payload {
		t.Fatalf("decompressed payload mismatch")
	}
}

func TestClientUploadSQLiteBundleFilesUploadsPartsThenManifest(t *testing.T) {
	dir := t.TempDir()
	partPath := filepath.Join(dir, "part")
	if err := os.WriteFile(partPath, []byte("compressed"), 0o600); err != nil {
		t.Fatalf("write part: %v", err)
	}
	var uploads []string
	var sawManifest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads = append(uploads, r.Header.Get("x-crawl-sqlite-upload"))
		switch r.Header.Get("x-crawl-sqlite-upload") {
		case "bundle-part":
			if r.Header.Get("x-crawl-bundle-part-index") != "0" || r.Header.Get("content-type") != "application/gzip" {
				t.Fatalf("part headers index=%q content-type=%q", r.Header.Get("x-crawl-bundle-part-index"), r.Header.Get("content-type"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read part body: %v", err)
			}
			if string(body) != "compressed" {
				t.Fatalf("part body = %q", body)
			}
			_ = json.NewEncoder(w).Encode(SQLiteUploadResult{Complete: true})
		case "bundle-manifest":
			sawManifest = true
			var manifest SQLiteBundleManifest
			if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
			if manifest.Format != SQLiteGzipChunkedBundleFormat {
				t.Fatalf("manifest = %#v", manifest)
			}
			_ = json.NewEncoder(w).Encode(SQLiteBundleUploadResult{
				App:      "gitcrawl",
				Archive:  "gitcrawl/openclaw__openclaw",
				Complete: true,
				Bundle:   &SQLiteBundle{Key: SQLiteBundleManifestKey("gitcrawl", "gitcrawl/openclaw__openclaw"), Manifest: &manifest},
			})
		default:
			t.Fatalf("unexpected upload kind %q", r.Header.Get("x-crawl-sqlite-upload"))
		}
	}))
	defer server.Close()
	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret"), UserAgent: "test-agent"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	result, err := client.UploadSQLiteBundleFiles(context.Background(), "gitcrawl", "gitcrawl/openclaw__openclaw", SQLiteBundleManifest{
		Format:  SQLiteGzipChunkedBundleFormat,
		App:     "gitcrawl",
		Archive: "gitcrawl/openclaw__openclaw",
	}, []SQLiteBundlePartFile{{
		SQLiteBundlePart: SQLiteBundlePart{Index: 0, Size: int64(len("compressed")), SHA256: "part-sha"},
		Path:             partPath,
	}})
	if err != nil {
		t.Fatalf("upload bundle files: %v", err)
	}
	if !sawManifest || len(uploads) != 2 || uploads[0] != "bundle-part" || uploads[1] != "bundle-manifest" {
		t.Fatalf("uploads = %#v sawManifest=%v", uploads, sawManifest)
	}
	if result.Bundle == nil || result.Bundle.Manifest == nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientErrorDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden","message":"wrong team"}`))
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = client.Whoami(context.Background())
	var remoteErr *Error
	if !errors.As(err, &remoteErr) {
		t.Fatalf("err = %T %v", err, err)
	}
	if remoteErr.Status != http.StatusForbidden || remoteErr.Code != "forbidden" || remoteErr.Message != "wrong team" {
		t.Fatalf("remote err = %#v", remoteErr)
	}
}

func TestClientFromConfigUsesEnvToken(t *testing.T) {
	t.Setenv("CRAWL_REMOTE_FROM_CONFIG", "env-token")
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("authorization")
		_ = json.NewEncoder(w).Encode(Identity{Owner: "owner@example.com", Org: "openclaw"})
	}))
	defer server.Close()

	cfg := Config{Mode: ModeCloud, Endpoint: server.URL, TokenEnv: "CRAWL_REMOTE_FROM_CONFIG"}
	client, err := NewClientFromConfig(cfg, Options{})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.Whoami(context.Background()); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if auth != "Bearer env-token" {
		t.Fatalf("auth = %q", auth)
	}
}

func TestBaseContractValidates(t *testing.T) {
	contract := BaseContract()
	if err := contract.Validate(); err != nil {
		t.Fatalf("contract validate: %v", err)
	}
	if contract.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %q", contract.ProtocolVersion)
	}
	if !hasRoute(contract, http.MethodGet, ContractPath, AuthPublic) {
		t.Fatalf("contract route missing")
	}
}

func TestClientContractIsPublic(t *testing.T) {
	var sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ContractPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		sawAuth = r.Header.Get("authorization")
		_ = json.NewEncoder(w).Encode(testServiceContract())
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	contract, err := client.Contract(context.Background())
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if sawAuth != "" {
		t.Fatalf("contract should not send authorization header, got %q", sawAuth)
	}
	if err := contract.Validate(); err != nil {
		t.Fatalf("validate response: %v", err)
	}
}

func TestChainTokenProviderSkipsNilAndUsesFirstToken(t *testing.T) {
	t.Setenv("CRAWL_REMOTE_CHAIN_TOKEN", "chain-token")
	provider := ChainTokenProvider{nil, EnvTokenProvider{Name: "CRAWL_REMOTE_CHAIN_TOKEN"}, StaticToken("fallback")}
	token, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token != "chain-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoginPollSecretHash(t *testing.T) {
	secret, err := NewLoginPollSecret()
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if secret == "" {
		t.Fatal("secret is empty")
	}
	if got := LoginPollSecretHash(" poll-secret "); got != LoginPollSecretHash("poll-secret") {
		t.Fatalf("hash should trim surrounding spaces")
	}
	if got := LoginPollSecretHash("poll-secret"); got != "0e3e16e9ef6f0c4887962402b8af7242b241128b711567a0baff5902dd3540b8" {
		t.Fatalf("hash = %q", got)
	}
}

func TestLoginWithGitHubToken(t *testing.T) {
	var sawToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/github/token" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var req GitHubTokenLoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		sawToken = req.Token
		_ = json.NewEncoder(w).Encode(LoginPollResult{
			Status: "complete",
			Token:  "session-token",
			Org:    "openclaw",
			Login:  "alice",
		})
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	result, err := client.LoginWithGitHubToken(context.Background(), " github-token ")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if sawToken != "github-token" {
		t.Fatalf("github token = %q", sawToken)
	}
	if result.Status != "complete" || result.Token != "session-token" || result.Login != "alice" {
		t.Fatalf("result = %#v", result)
	}
}

func TestStaticTokenRejectsBlank(t *testing.T) {
	_, err := StaticToken(" ").Token(context.Background())
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("err = %v", err)
	}
}

func hasRoute(contract Contract, method, path, auth string) bool {
	for _, route := range contract.Routes {
		if route.Method == method && route.Path == path && route.Auth == auth {
			return true
		}
	}
	return false
}

func testServiceContract() Contract {
	contract := BaseContract()
	contract.Apps = []AppSpec{{
		App: "examplecrawl",
		Queries: []QuerySpec{
			{Name: "example.items.search", Args: []string{"query"}},
		},
		IngestTables: []IngestTableSpec{
			{Name: "items", Columns: []string{"id", "title", "updated_at"}},
		},
		Capabilities: []string{"example.items.search"},
	}}
	return contract
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
