package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
