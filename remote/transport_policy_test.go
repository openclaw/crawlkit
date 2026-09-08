package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func credentialCall(client *Client, kind string) error {
	switch kind {
	case "bearer":
		_, err := client.Whoami(context.Background())
		return err
	case "login":
		_, err := client.LoginWithGitHubToken(context.Background(), "synthetic-login-token")
		return err
	default:
		_, err := client.PollGitHubLogin(context.Background(), "login-id", "synthetic-poll-secret")
		return err
	}
}

func TestCredentialRequestsRejectRemoteHTTPWithoutProvider(t *testing.T) {
	for _, kind := range []string{"login", "poll"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			client, err := NewClient(Options{
				Endpoint: "http://archive.example.invalid",
				HTTPClient: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := credentialCall(client, kind); err == nil {
				t.Error("credential-bearing remote HTTP request accepted")
			}
			if calls != 0 {
				t.Fatalf("unsafe transport called %d times", calls)
			}
		})
	}
}

func TestCredentialRedirectsStayOnSecureOrigin(t *testing.T) {
	for _, kind := range []string{"bearer", "login", "poll"} {
		for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			for _, downgrade := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/downgrade=%v", kind, code, downgrade), func(t *testing.T) {
					var received atomic.Int32
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						received.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						_, _ = io.WriteString(w, "{}")
					})
					var target *httptest.Server
					if downgrade {
						target = httptest.NewServer(handler)
					} else {
						target = httptest.NewTLSServer(handler)
					}
					defer target.Close()
					source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						http.Redirect(w, r, target.URL+"/receive", code)
					}))
					defer source.Close()
					client, err := NewClient(Options{Endpoint: source.URL, HTTPClient: source.Client(), TokenProvider: StaticToken("synthetic-bearer")})
					if err != nil {
						t.Fatal(err)
					}
					if err := credentialCall(client, kind); err == nil {
						t.Error("credential redirect accepted")
					}
					if got := received.Load(); got != 0 {
						t.Fatalf("redirect receiver called %d times", got)
					}
				})
			}
		}
	}
}

func TestCredentialRedirectPreservesCallerPolicyAndClient(t *testing.T) {
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/finish" {
			http.Redirect(w, r, "/finish", http.StatusTemporaryRedirect)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-bearer" {
			t.Error("same-origin redirect lost bearer")
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer source.Close()
	httpClient := source.Client()
	client, err := NewClient(Options{Endpoint: source.URL, HTTPClient: httpClient, TokenProvider: StaticToken("synthetic-bearer")})
	if err != nil {
		t.Fatal(err)
	}
	if err := credentialCall(client, "bearer"); err != nil {
		t.Fatalf("same-origin redirect failed: %v", err)
	}
	if httpClient.CheckRedirect != nil {
		t.Fatal("caller HTTP client was mutated")
	}
	blocked := errors.New("caller refused redirect")
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return blocked }
	if err := credentialCall(client, "bearer"); !errors.Is(err, blocked) {
		t.Fatalf("caller policy lost: %v", err)
	}
}

func TestCredentialRedirectRechecksCallerModifiedURL(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, _ = io.WriteString(w, "{}")
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/finish", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	httpClient := source.Client()
	httpClient.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		req.URL, _ = url.Parse(target.URL)
		return nil
	}
	client, err := NewClient(Options{Endpoint: source.URL, HTTPClient: httpClient, TokenProvider: StaticToken("synthetic-bearer")})
	if err != nil {
		t.Fatal(err)
	}
	if err := credentialCall(client, "bearer"); err == nil || received.Load() != 0 {
		t.Fatalf("caller-modified unsafe redirect reached transport: error=%v calls=%d", err, received.Load())
	}
}

func TestCredentialRedirectRetainsSameOriginBodies(t *testing.T) {
	for _, kind := range []string{"login", "poll"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/finish" {
					http.Redirect(w, r, "/finish", http.StatusPermanentRedirect)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(body), "synthetic-") || r.Method != http.MethodPost {
					t.Errorf("same-origin body/method lost: %s %q, %v", r.Method, body, err)
				}
				_, _ = io.WriteString(w, "{}")
			}))
			defer server.Close()
			client, err := NewClient(Options{Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if err := credentialCall(client, kind); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAnonymousRedirectPolicyIsUnchanged(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{}")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client, err := NewClient(Options{Endpoint: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Contract(context.Background()); err != nil {
		t.Fatalf("anonymous cross-origin contract redirect changed: %v", err)
	}
}

func TestCredentialOriginUsesDefaultPorts(t *testing.T) {
	left, _ := url.Parse("https://example.invalid/a")
	right, _ := url.Parse("https://EXAMPLE.invalid:443/b")
	if !sameOrigin(left, right) {
		t.Fatal("explicit default port changed origin")
	}
	right.Host = "example.invalid:444"
	if sameOrigin(left, right) {
		t.Fatal("different explicit port retained origin")
	}
}
