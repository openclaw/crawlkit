package remote

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
		if req.App != "gitcrawl" || req.Archive != "gitcrawl/openclaw__openclaw" ||
			req.Name != "gitcrawl.threads.search" || req.SnapshotID != strings.Repeat("a", 64) {
			t.Fatalf("request = %#v", req)
		}
		_ = json.NewEncoder(w).Encode(QueryResult{
			Columns: []string{"number", "title"},
			Rows:    [][]any{{float64(1), "remote"}},
			Stats: QueryStats{
				SnapshotID:       strings.Repeat("a", 64),
				CoverageComplete: true,
				SchemaVersion:    8,
				ObservationOrder: "observation-order",
				NextCursor:       "cursor-2",
			},
			Snapshot: &ArchiveSnapshot{
				ID:           strings.Repeat("a", 64),
				SourceSHA256: strings.Repeat("a", 64),
				PublishedAt:  "2026-07-12T08:00:00Z",
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret"), UserAgent: "test-agent"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	result, err := client.Query(context.Background(), "gitcrawl", "gitcrawl/openclaw__openclaw", QueryRequest{
		Name:       "gitcrawl.threads.search",
		SnapshotID: strings.Repeat("a", 64),
	})
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
	if result.Snapshot == nil || result.Snapshot.ID != strings.Repeat("a", 64) {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Stats.SnapshotID != result.Snapshot.ID || result.Stats.NextCursor != "cursor-2" {
		t.Fatalf("stats = %#v", result.Stats)
	}
}

func TestClientRejectsBearerTokenOverRemoteHTTP(t *testing.T) {
	_, err := NewClient(Options{Endpoint: "http://remote.example", TokenProvider: StaticToken("secret")})
	if err == nil {
		t.Fatal("expected plaintext remote auth error")
	}
	if !strings.Contains(err.Error(), "bearer auth over http") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientArchiveOperations(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		if r.URL.Path != "/v1/auth/github/start" && r.URL.Path != "/v1/auth/github/poll" && r.Header.Get("authorization") != "Bearer secret" {
			t.Fatalf("auth = %q", r.Header.Get("authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/archives":
			_ = json.NewEncoder(w).Encode(map[string]any{"archives": []Archive{{
				ID:       "arch-1",
				App:      "gitcrawl",
				Snapshot: &ArchiveSnapshot{ID: strings.Repeat("b", 64)},
			}}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(Status{
				App:                "gitcrawl",
				Archive:            "gitcrawl/openclaw",
				Mode:               ModeCloud,
				SchemaVersion:      8,
				SnapshotMode:       "snapshot",
				SnapshotCutoverAt:  "2026-07-12T08:00:00Z",
				ActiveSnapshotID:   strings.Repeat("b", 64),
				SourceSyncAt:       "2026-07-12T07:55:00Z",
				DatasetGeneratedAt: "2026-07-12T07:56:00Z",
				CoverageComplete:   true,
				Datasets: []DatasetCoverage{{
					Dataset:  "threads",
					RowCount: 10,
					Complete: true,
				}},
				Snapshot: &ArchiveSnapshot{ID: strings.Repeat("b", 64)},
				Publish:  &ArchivePublish{Status: "complete"},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/batch-read"):
			var body struct {
				Requests []QueryRequest `json:"requests"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode batch-read: %v", err)
			}
			if len(body.Requests) != 1 || body.Requests[0].App != "gitcrawl" || body.Requests[0].Archive != "gitcrawl/openclaw" {
				t.Fatalf("batch request = %#v", body.Requests)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []QueryResult{{Columns: []string{"id"}, Rows: [][]any{{"1"}}}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ingest"):
			var req IngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode ingest: %v", err)
			}
			if req.Manifest.App != "gitcrawl" || req.Manifest.Archive != "gitcrawl/openclaw" {
				t.Fatalf("ingest manifest = %#v", req.Manifest)
			}
			if req.Manifest.SnapshotID != strings.Repeat("b", 64) || req.Manifest.SourceSHA256 != strings.Repeat("b", 64) {
				t.Fatalf("ingest snapshot manifest = %#v", req.Manifest)
			}
			if req.MutationToken != "generation-1" {
				t.Fatalf("mutation token = %q", req.MutationToken)
			}
			if len(req.Rows) == 0 {
				_ = json.NewEncoder(w).Encode(IngestResult{RunID: "run-1", Table: req.Table, ResetIncomplete: true, ResetDeleted: 10000})
				return
			}
			_ = json.NewEncoder(w).Encode(IngestResult{
				RunID:         "run-1",
				Table:         req.Table,
				SnapshotID:    req.Manifest.SnapshotID,
				MutationToken: req.MutationToken,
				RowsAccepted:  int64(len(req.Rows)),
				Complete:      req.Final,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cutover"):
			var body struct {
				SnapshotID string `json:"snapshot_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode cutover: %v", err)
			}
			_ = json.NewEncoder(w).Encode(CutoverResult{
				Archive:      "gitcrawl/openclaw",
				SnapshotID:   body.SnapshotID,
				SnapshotMode: "explicit",
				CutoverAt:    "2026-07-12T08:00:00Z",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/github/start":
			var req LoginStartRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode login start: %v", err)
			}
			if req.PollSecretHash != "hash" {
				t.Fatalf("poll secret hash = %q", req.PollSecretHash)
			}
			_ = json.NewEncoder(w).Encode(LoginStartResult{LoginID: "login-1", URL: "https://login.example"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/github/poll":
			var req LoginPollRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode login poll: %v", err)
			}
			if req.LoginID != "login-1" || req.PollSecret != "secret" {
				t.Fatalf("login poll request = %#v", req)
			}
			_ = json.NewEncoder(w).Encode(LoginPollResult{Status: "complete", Token: "session-token"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret"), UserAgent: "test-agent"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	archives, err := client.Archives(context.Background())
	if err != nil {
		t.Fatalf("archives: %v", err)
	}
	if len(archives) != 1 || archives[0].ID != "arch-1" || archives[0].Snapshot == nil {
		t.Fatalf("archives = %#v", archives)
	}
	status, err := client.Status(context.Background(), "gitcrawl", "gitcrawl/openclaw")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Mode != ModeCloud || status.Snapshot == nil || status.Publish == nil ||
		status.ActiveSnapshotID == "" || !status.CoverageComplete || len(status.Datasets) != 1 {
		t.Fatalf("status = %#v", status)
	}
	results, err := client.BatchRead(context.Background(), "gitcrawl", "gitcrawl/openclaw", []QueryRequest{{Name: "threads"}})
	if err != nil {
		t.Fatalf("batch read: %v", err)
	}
	if len(results) != 1 || results[0].Columns[0] != "id" {
		t.Fatalf("batch results = %#v", results)
	}
	manifest := IngestManifest{
		SnapshotID:   strings.Repeat("b", 64),
		SourceSHA256: strings.Repeat("b", 64),
	}
	ingest, err := client.Ingest(context.Background(), "gitcrawl", "gitcrawl/openclaw", IngestRequest{
		Manifest:      manifest,
		Table:         "threads",
		Rows:          [][]any{{"1"}},
		MutationToken: "generation-1",
		Final:         true,
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !ingest.Complete || ingest.RowsAccepted != 1 || ingest.SnapshotID != manifest.SnapshotID ||
		ingest.MutationToken != "generation-1" {
		t.Fatalf("ingest result = %#v", ingest)
	}
	reset, err := client.Ingest(context.Background(), "gitcrawl", "gitcrawl/openclaw", IngestRequest{
		Manifest:      manifest,
		Table:         "threads",
		Rows:          [][]any{},
		MutationToken: "generation-1",
	})
	if err != nil {
		t.Fatalf("reset ingest: %v", err)
	}
	if !reset.ResetIncomplete || reset.ResetDeleted != 10000 {
		t.Fatalf("reset result = %#v", reset)
	}
	cutover, err := client.Cutover(context.Background(), "gitcrawl", "gitcrawl/openclaw", manifest.SnapshotID)
	if err != nil {
		t.Fatalf("cutover: %v", err)
	}
	if cutover.Archive != "gitcrawl/openclaw" ||
		cutover.SnapshotID != manifest.SnapshotID ||
		cutover.SnapshotMode != "explicit" {
		t.Fatalf("cutover result = %#v", cutover)
	}
	start, err := client.StartGitHubLogin(context.Background(), "hash")
	if err != nil {
		t.Fatalf("start login: %v", err)
	}
	if start.LoginID != "login-1" {
		t.Fatalf("start = %#v", start)
	}
	poll, err := client.PollGitHubLogin(context.Background(), "login-1", "secret")
	if err != nil {
		t.Fatalf("poll login: %v", err)
	}
	if poll.Status != "complete" || poll.Token != "session-token" {
		t.Fatalf("poll = %#v", poll)
	}
	if len(requests) != 8 {
		t.Fatalf("requests = %#v", requests)
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

func TestBuildGzipSQLiteBundlePreservesCurrentKeyLayout(t *testing.T) {
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
	if bundle.Manifest.SnapshotID != "" {
		t.Fatalf("snapshot id = %q", bundle.Manifest.SnapshotID)
	}
	if bundle.Manifest.Object.Key != SQLiteObjectKey("gitcrawl", "gitcrawl/openclaw__openclaw") {
		t.Fatalf("object key = %q", bundle.Manifest.Object.Key)
	}
	if bundle.Manifest.CompressedObject.Key != SQLiteCompressedObjectKey("gitcrawl", "gitcrawl/openclaw__openclaw") {
		t.Fatalf("compressed object key = %q", bundle.Manifest.CompressedObject.Key)
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
		if part.Key != SQLiteBundlePartKey("gitcrawl", "gitcrawl/openclaw__openclaw", part.Index) {
			t.Fatalf("part key = %q", part.Key)
		}
	}
	assertSQLiteBundlePayload(t, compressed.String(), payload)
}

func TestBuildSnapshotGzipSQLiteBundleUsesSnapshotKeyLayout(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "archive.db")
	payload := strings.Repeat("SQLite format 3\n", 100)
	if err := os.WriteFile(source, []byte(payload), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	bundle, err := BuildSnapshotGzipSQLiteBundle(context.Background(), SQLiteBundleBuildOptions{
		App:        "gitcrawl",
		Archive:    "gitcrawl/openclaw__openclaw",
		SourcePath: source,
		WorkDir:    dir,
		ChunkSize:  64,
		Counts:     map[string]int64{"threads": 3},
	})
	if err != nil {
		t.Fatalf("build snapshot bundle: %v", err)
	}
	defer bundle.Cleanup()
	if bundle.Manifest.SnapshotID == "" || bundle.Manifest.SnapshotID != bundle.Manifest.Object.SHA256 {
		t.Fatalf("snapshot id = %q object sha = %q", bundle.Manifest.SnapshotID, bundle.Manifest.Object.SHA256)
	}
	if bundle.Manifest.Object.Key != SQLiteSnapshotObjectKey(
		"gitcrawl",
		"gitcrawl/openclaw__openclaw",
		bundle.Manifest.SnapshotID,
	) {
		t.Fatalf("object key = %q", bundle.Manifest.Object.Key)
	}
	if bundle.Manifest.CompressedObject.Key != SQLiteSnapshotCompressedObjectKey(
		"gitcrawl",
		"gitcrawl/openclaw__openclaw",
		bundle.Manifest.SnapshotID,
		bundle.Manifest.CompressedObject.SHA256,
	) {
		t.Fatalf("compressed object key = %q", bundle.Manifest.CompressedObject.Key)
	}
	var compressed strings.Builder
	for _, part := range bundle.Parts {
		bytes, err := os.ReadFile(part.Path)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		compressed.Write(bytes)
		if part.Key != SQLiteSnapshotBundlePartKey(
			"gitcrawl",
			"gitcrawl/openclaw__openclaw",
			bundle.Manifest.SnapshotID,
			part.SHA256,
			part.Index,
		) {
			t.Fatalf("part key = %q", part.Key)
		}
	}
	decompressed := assertSQLiteBundlePayload(t, compressed.String(), payload)
	if got, want := bundle.Manifest.SnapshotID, fmt.Sprintf("%x", sha256.Sum256(decompressed)); got != want {
		t.Fatalf("snapshot id = %q, want digest of compressed source bytes %q", got, want)
	}
}

func TestBuildSnapshotGzipSQLiteBundleIsolatesEncodedRepresentations(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "archive.db")
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte((i*31 + i/7) % 251)
	}
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	build := func(t *testing.T, level int, chunkSize int64) SQLiteBundleBuild {
		t.Helper()
		bundle, err := BuildSnapshotGzipSQLiteBundle(context.Background(), SQLiteBundleBuildOptions{
			App:              "gitcrawl",
			Archive:          "gitcrawl/openclaw__openclaw",
			SourcePath:       source,
			WorkDir:          dir,
			CompressionLevel: level,
			ChunkSize:        chunkSize,
		})
		if err != nil {
			t.Fatalf("build snapshot bundle: %v", err)
		}
		t.Cleanup(bundle.Cleanup)
		return bundle
	}
	fastSmall := build(t, gzip.BestSpeed, 64)
	bestSmall := build(t, gzip.BestCompression, 64)
	fastLarge := build(t, gzip.BestSpeed, 128)
	if fastSmall.Manifest.SnapshotID != bestSmall.Manifest.SnapshotID ||
		fastSmall.Manifest.SnapshotID != fastLarge.Manifest.SnapshotID {
		t.Fatalf("source snapshot ids differ")
	}
	if fastSmall.Manifest.Object.Key != bestSmall.Manifest.Object.Key ||
		fastSmall.Manifest.Object.Key != fastLarge.Manifest.Object.Key {
		t.Fatalf("source object keys differ")
	}
	if fastSmall.Manifest.CompressedObject.Key == bestSmall.Manifest.CompressedObject.Key {
		t.Fatalf("compression variants share key %q", fastSmall.Manifest.CompressedObject.Key)
	}
	if fastSmall.Manifest.Parts[0].Key == fastLarge.Manifest.Parts[0].Key {
		t.Fatalf("chunk variants share first part key %q", fastSmall.Manifest.Parts[0].Key)
	}
}

func assertSQLiteBundlePayload(t *testing.T, compressed, payload string) []byte {
	t.Helper()
	reader, err := gzip.NewReader(strings.NewReader(compressed))
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
	return decompressed
}

func TestSQLiteBundleKeyLayouts(t *testing.T) {
	const (
		app      = "git crawl"
		archive  = "openclaw/crawlkit"
		snapshot = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	if got, want := SQLiteObjectKey(app, archive), "v1/git%20crawl/openclaw%2Fcrawlkit/sqlite/current.db"; got != want {
		t.Fatalf("object key = %q, want %q", got, want)
	}
	if got, want := SQLiteCompressedObjectKey(app, archive), "v1/git%20crawl/openclaw%2Fcrawlkit/sqlite/current.db.gz"; got != want {
		t.Fatalf("compressed key = %q, want %q", got, want)
	}
	if got, want := SQLiteBundleManifestKey(app, archive), "v1/git%20crawl/openclaw%2Fcrawlkit/sqlite/current.manifest.json"; got != want {
		t.Fatalf("manifest key = %q, want %q", got, want)
	}
	if got, want := SQLiteBundlePartKey(app, archive, 7), "v1/git%20crawl/openclaw%2Fcrawlkit/sqlite/chunks/current.db.gz.part-0007"; got != want {
		t.Fatalf("part key = %q, want %q", got, want)
	}
	if got := SQLiteSnapshotObjectKey(app, archive, ""); got != SQLiteObjectKey(app, archive) {
		t.Fatalf("empty snapshot object key = %q", got)
	}
	if got := SQLiteSnapshotCompressedObjectKey(app, archive, "", ""); got != SQLiteCompressedObjectKey(app, archive) {
		t.Fatalf("empty snapshot compressed key = %q", got)
	}
	if got := SQLiteSnapshotBundleManifestKey(app, archive, ""); got != SQLiteBundleManifestKey(app, archive) {
		t.Fatalf("empty snapshot manifest key = %q", got)
	}
	if got := SQLiteSnapshotBundlePartKey(app, archive, "", "", 7); got != SQLiteBundlePartKey(app, archive, 7) {
		t.Fatalf("empty snapshot part key = %q", got)
	}
	if got := SQLiteSnapshotObjectKey(app, archive, "not-a-digest"); got != "" {
		t.Fatalf("invalid snapshot object key = %q", got)
	}
	if got := SQLiteSnapshotCompressedObjectKey(app, archive, snapshot, "not-a-digest"); got != "" {
		t.Fatalf("invalid compressed object key = %q", got)
	}
	if got := SQLiteSnapshotBundlePartKey(app, archive, snapshot, "not-a-digest", 7); got != "" {
		t.Fatalf("invalid snapshot part key = %q", got)
	}
	if got := SQLiteSnapshotBundleManifestKey(app, archive, strings.ToUpper(snapshot)); got != "" {
		t.Fatalf("non-canonical snapshot manifest key = %q", got)
	}
}

func TestClientUploadSQLiteBundleFilesPreservesAddressingMode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		snapshotID  string
		manifestKey func() string
	}{
		{
			name: "legacy",
			manifestKey: func() string {
				return SQLiteBundleManifestKey("gitcrawl", "gitcrawl/openclaw__openclaw")
			},
		},
		{
			name:       "snapshot",
			snapshotID: strings.Repeat("c", 64),
			manifestKey: func() string {
				return SQLiteSnapshotBundleManifestKey("gitcrawl", "gitcrawl/openclaw__openclaw", strings.Repeat("c", 64))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			partPath := filepath.Join(dir, "part")
			if err := os.WriteFile(partPath, []byte("compressed"), 0o600); err != nil {
				t.Fatalf("write part: %v", err)
			}
			var uploads []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uploads = append(uploads, r.Header.Get("x-crawl-sqlite-upload"))
				switch r.Header.Get("x-crawl-sqlite-upload") {
				case "bundle-part":
					if r.Header.Get("x-crawl-bundle-part-index") != "0" ||
						r.Header.Get("content-type") != "application/gzip" ||
						r.Header.Get("x-crawl-content-sha256") != strings.Repeat("d", 64) ||
						r.Header.Get("x-crawl-compression") != SQLiteGzipCompression ||
						r.Header.Get("x-crawl-snapshot-id") != tc.snapshotID {
						t.Fatalf(
							"part headers index=%q content-type=%q sha=%q compression=%q snapshot=%q",
							r.Header.Get("x-crawl-bundle-part-index"),
							r.Header.Get("content-type"),
							r.Header.Get("x-crawl-content-sha256"),
							r.Header.Get("x-crawl-compression"),
							r.Header.Get("x-crawl-snapshot-id"),
						)
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
					var manifest SQLiteBundleManifest
					if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
						t.Fatalf("decode manifest: %v", err)
					}
					if manifest.Format != SQLiteGzipChunkedBundleFormat || manifest.SnapshotID != tc.snapshotID {
						t.Fatalf("manifest = %#v", manifest)
					}
					_ = json.NewEncoder(w).Encode(SQLiteBundleUploadResult{
						App:      "gitcrawl",
						Archive:  "gitcrawl/openclaw__openclaw",
						Complete: true,
						Bundle: &SQLiteBundle{
							Key:      tc.manifestKey(),
							Manifest: &manifest,
						},
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
				Format:     SQLiteGzipChunkedBundleFormat,
				App:        "gitcrawl",
				Archive:    "gitcrawl/openclaw__openclaw",
				SnapshotID: tc.snapshotID,
			}, []SQLiteBundlePartFile{{
				SQLiteBundlePart: SQLiteBundlePart{Index: 0, Size: int64(len("compressed")), SHA256: strings.Repeat("d", 64)},
				Path:             partPath,
			}})
			if err != nil {
				t.Fatalf("upload bundle files: %v", err)
			}
			if len(uploads) != 2 || uploads[0] != "bundle-part" || uploads[1] != "bundle-manifest" {
				t.Fatalf("uploads = %#v", uploads)
			}
			if result.Bundle == nil || result.Bundle.Key != tc.manifestKey() {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestClientRejectsInvalidSQLiteBundleSnapshotIDs(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("unexpected request")
	}))
	defer server.Close()
	client, err := NewClient(Options{Endpoint: server.URL, TokenProvider: StaticToken("secret")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	invalid := strings.Repeat("g", 64)
	if _, err := client.UploadSQLiteBundlePart(context.Background(), "gitcrawl", "archive", SQLiteBundlePartUpload{
		Body:       strings.NewReader("compressed"),
		Size:       int64(len("compressed")),
		SnapshotID: invalid,
	}); err == nil || !strings.Contains(err.Error(), "lowercase sha256 digest") {
		t.Fatalf("part upload err = %v", err)
	}
	if _, err := client.UploadSQLiteBundleManifest(context.Background(), "gitcrawl", "archive", SQLiteBundleManifest{
		SnapshotID: invalid,
	}); err == nil || !strings.Contains(err.Error(), "lowercase sha256 digest") {
		t.Fatalf("manifest upload err = %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
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
	if !hasRoute(contract, http.MethodPost, "/v1/apps/:app/archives/:archive/cutover", AuthPublisher) {
		t.Fatalf("contract cutover route missing")
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
