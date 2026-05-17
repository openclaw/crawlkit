package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOllamaProviderEmbeds(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		var req ollamaEmbedRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "nomic-embed-text", req.Model)
		assert.Equal(t, []string{"abcd", "xy"}, req.Input)
		_, _ = w.Write([]byte(`{"model":"nomic-embed-text","embeddings":[[1,2,3],[4,5,6]]}`))
	}))
	defer server.Close()

	provider, err := NewProvider(Config{
		Provider:       ProviderOllama,
		Model:          "nomic-embed-text",
		BaseURL:        server.URL,
		MaxInputChars:  4,
		RequestTimeout: "5s",
	})
	require.NoError(t, err)

	batch, err := provider.Embed(context.Background(), []string{"abcdef", "xy"})
	require.NoError(t, err)
	require.Equal(t, "nomic-embed-text", batch.Model)
	require.Equal(t, 3, batch.Dimensions)
	require.Equal(t, [][]float32{{1, 2, 3}, {4, 5, 6}}, batch.Vectors)
}

type assertAPI struct{}
type requireAPI struct{}

var assert assertAPI
var require requireAPI

func (assertAPI) Equal(t *testing.T, want, got any) bool {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("not equal:\nwant: %#v\n got: %#v", want, got)
		return false
	}
	return true
}

func (assertAPI) NoError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return false
	}
	return true
}

func (requireAPI) NoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func (requireAPI) Equal(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not equal:\nwant: %#v\n got: %#v", want, got)
	}
}

func (requireAPI) Same(t *testing.T, want, got any) {
	t.Helper()
	if !reflect.ValueOf(want).IsValid() || !reflect.ValueOf(got).IsValid() ||
		reflect.ValueOf(want).Pointer() != reflect.ValueOf(got).Pointer() {
		t.Fatalf("not same:\nwant: %#v\n got: %#v", want, got)
	}
}

func (requireAPI) True(t *testing.T, value bool) {
	t.Helper()
	if !value {
		t.Fatal("expected true")
	}
}

func (requireAPI) False(t *testing.T, value bool) {
	t.Helper()
	if value {
		t.Fatal("expected false")
	}
}

func (requireAPI) Empty(t *testing.T, value string) {
	t.Helper()
	if value != "" {
		t.Fatalf("expected empty string, got %q", value)
	}
}

func (requireAPI) Contains(t *testing.T, value, needle string) {
	t.Helper()
	if !strings.Contains(value, needle) {
		t.Fatalf("expected %q to contain %q", value, needle)
	}
}

func (requireAPI) ErrorContains(t *testing.T, err error, needle string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", needle)
	}
	if !strings.Contains(err.Error(), needle) {
		t.Fatalf("expected error containing %q, got %q", needle, err.Error())
	}
}

func TestOpenAICompatibleProviderEmbedsAndUsesAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		var req openAIEmbeddingRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "local-model", req.Model)
		assert.Equal(t, []string{"one", "two"}, req.Input)
		_, _ = w.Write([]byte(`{
			"model":"local-model",
			"data":[
				{"index":1,"embedding":[3,4]},
				{"index":0,"embedding":[1,2]}
			]
		}`))
	}))
	defer server.Close()
	t.Setenv("CRAWLKIT_EMBED_KEY", "secret")

	provider, err := NewProvider(Config{
		Provider:       ProviderOpenAICompatible,
		Model:          "local-model",
		BaseURL:        server.URL,
		APIKeyEnv:      "CRAWLKIT_EMBED_KEY",
		RequestTimeout: "5s",
	})
	require.NoError(t, err)

	batch, err := provider.Embed(context.Background(), []string{"one", "two"})
	require.NoError(t, err)
	require.Equal(t, "local-model", batch.Model)
	require.Equal(t, 2, batch.Dimensions)
	require.Equal(t, [][]float32{{1, 2}, {3, 4}}, batch.Vectors)
	require.Equal(t, [][]float64{{1, 2}, {3, 4}}, batch.Vectors64)
}

func TestOpenAICompatibleProviderSupportsDimensionsAndRawAPIKey(t *testing.T) {
	var gotAuth string
	var gotUserAgent string
	var gotDimensions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		var req openAIEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotDimensions = req.Dimensions
		_ = json.NewEncoder(w).Encode(openAIEmbeddingResponse{
			Model: "model",
			Data:  []openAIEmbeddingItem{{Embedding: []float64{0.1, 0.2}}},
		})
	}))
	defer server.Close()

	provider, err := NewProvider(Config{
		Provider:   ProviderOpenAI,
		Model:      "model",
		BaseURL:    server.URL,
		Dimensions: 1024,
	}, WithAPIKey("raw-key"), WithUserAgent("gitcrawl"))
	require.NoError(t, err)
	batch, err := provider.Embed(context.Background(), []string{"hello"})
	require.NoError(t, err)

	require.Equal(t, "Bearer raw-key", gotAuth)
	require.Equal(t, "gitcrawl", gotUserAgent)
	require.Equal(t, 1024, gotDimensions)
	require.Equal(t, 2, batch.Dimensions)
	require.Equal(t, [][]float64{{0.1, 0.2}}, batch.Vectors64)
	require.Equal(t, [][]float32{{0.1, 0.2}}, batch.Vectors)
}

func TestOpenAICompatibleHTTPErrorIncludesHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"limited"}}`))
	}))
	defer server.Close()

	provider, err := NewProvider(Config{Provider: ProviderOpenAI, BaseURL: server.URL}, WithAPIKey("raw-key"))
	require.NoError(t, err)
	_, err = provider.Embed(context.Background(), []string{"hello"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %T %v, want HTTPError", err, err)
	}
	require.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
	require.Equal(t, "2", httpErr.Header.Get("Retry-After"))
	require.Contains(t, httpErr.Body, "limited")
}

func TestProviderFactoryDefaultsAndValidation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-secret")

	openAI, err := resolveProviderConfig(Config{
		Provider:       ProviderOpenAI,
		RequestTimeout: "5s",
	}, true)
	require.NoError(t, err)
	require.Equal(t, DefaultOpenAIBaseURL, openAI.BaseURL)
	require.Equal(t, DefaultOpenAIModel, openAI.Model)
	require.Equal(t, "openai-secret", openAI.APIKey)

	ollama, err := resolveProviderConfig(Config{
		Provider:       ProviderOllama,
		RequestTimeout: "5s",
	}, true)
	require.NoError(t, err)
	require.Equal(t, DefaultOllamaBaseURL, ollama.BaseURL)
	require.Equal(t, DefaultLocalEmbeddingModel, ollama.Model)

	llamaCpp, err := resolveProviderConfig(Config{
		Provider:       ProviderLlamaCpp,
		RequestTimeout: "5s",
	}, true)
	require.NoError(t, err)
	require.Equal(t, DefaultLlamaCppBaseURL, llamaCpp.BaseURL)

	_, err = resolveProviderConfig(Config{
		Provider:       ProviderOpenAICompatible,
		RequestTimeout: "5s",
	}, true)
	require.ErrorContains(t, err, "requires base_url")
}

func TestProviderFactoryRequiresOpenAIAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	_, err := NewProvider(Config{
		Provider:       ProviderOpenAI,
		RequestTimeout: "5s",
	})
	require.ErrorContains(t, err, "requires API key env OPENAI_API_KEY")
}

func TestProviderFactoryReportsUnsupportedProviderBeforeAPIKey(t *testing.T) {
	t.Setenv("MISSING_EMBED_KEY", "")

	_, err := NewProvider(Config{
		Provider:       "bogus",
		APIKeyEnv:      "MISSING_EMBED_KEY",
		RequestTimeout: "5s",
	})
	require.ErrorContains(t, err, "unsupported embedding provider \"bogus\"")
}

func TestCheckProviderProbesLocalProvider(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		_, _ = w.Write([]byte(`{"model":"nomic-embed-text","embeddings":[[1,2]]}`))
	}))
	defer server.Close()

	result := CheckProvider(context.Background(), Config{
		Provider:       ProviderOllama,
		Model:          "nomic-embed-text",
		BaseURL:        server.URL,
		RequestTimeout: "5s",
	})
	require.Equal(t, "ok", result.Status)
	require.True(t, result.Probed)
	require.Empty(t, result.Warning)
	require.Equal(t, server.URL, result.BaseURL)
}

func TestCheckProviderWarnsOnLocalProbeFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	result := CheckProvider(context.Background(), Config{
		Provider:       ProviderOllama,
		Model:          "nomic-embed-text",
		BaseURL:        server.URL,
		RequestTimeout: "5s",
	})
	require.Equal(t, "warning", result.Status)
	require.Contains(t, result.Warning, "HTTP 503")
	require.False(t, result.Probed)
}

func TestProviderExposesRateLimitErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider, err := NewProvider(Config{
		Provider:       ProviderOpenAICompatible,
		Model:          "local-model",
		BaseURL:        server.URL,
		RequestTimeout: "5s",
	})
	require.NoError(t, err)

	_, err = provider.Embed(context.Background(), []string{"one"})
	require.ErrorContains(t, err, "HTTP 429")
	require.True(t, IsRateLimitError(err))
}

func TestProviderRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2,3]}]}`))
	}))
	defer server.Close()

	provider, err := NewProvider(Config{
		Provider:       ProviderOpenAICompatible,
		Model:          "local-model",
		BaseURL:        server.URL,
		RequestTimeout: "5s",
	})
	require.NoError(t, err)

	_, err = provider.Embed(context.Background(), []string{"one", "two"})
	require.ErrorContains(t, err, "dimensions mismatch")
}

func TestEmbeddingProvidersHandleEmptyInputsAndIndexErrors(t *testing.T) {
	t.Parallel()

	settings := providerSettings{
		Name:          ProviderOllama,
		Model:         "model",
		BaseURL:       "http://127.0.0.1:1",
		MaxInputChars: 10,
		HTTPClient:    http.DefaultClient,
	}
	ollama := newOllamaProvider(settings)
	batch, err := ollama.Embed(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "model", batch.Model)

	settings.Name = ProviderOpenAICompatible
	openai := newOpenAICompatibleProvider(settings)
	batch, err = openai.Embed(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "model", batch.Model)

	tests := []struct {
		name   string
		body   string
		inputs []string
		want   string
	}{
		{name: "count", body: `{"data":[]}`, inputs: []string{"one"}, want: "returned 0 vectors for 1 inputs"},
		{name: "range", body: `{"data":[{"index":2,"embedding":[1]}]}`, inputs: []string{"one"}, want: "index 2 out of range"},
		{name: "duplicate", body: `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`, inputs: []string{"one", "two"}, want: "duplicate index 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			provider, err := NewProvider(Config{
				Provider:       ProviderOpenAICompatible,
				Model:          "model",
				BaseURL:        server.URL,
				RequestTimeout: "5s",
			})
			require.NoError(t, err)
			_, err = provider.Embed(context.Background(), tc.inputs)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestProviderOptionsAndProbeDecisions(t *testing.T) {
	t.Parallel()

	client := &http.Client{Timeout: time.Second}
	settings, err := resolveProviderConfig(Config{
		Provider:       ProviderOllama,
		BaseURL:        "http://127.0.0.1:11434/",
		RequestTimeout: "30s",
	}, true, WithHTTPClient(client), WithRequestTimeout(50*time.Millisecond))
	require.NoError(t, err)
	require.Same(t, client, settings.HTTPClient)
	require.Equal(t, 50*time.Millisecond, settings.Timeout)
	require.Equal(t, "http://127.0.0.1:11434", settings.BaseURL)
	require.True(t, shouldProbe(settings))

	require.True(t, isLoopbackBaseURL("http://localhost:8080/v1"))
	require.True(t, isLoopbackBaseURL("http://[::1]:8080/v1"))
	require.False(t, isLoopbackBaseURL("https://api.example.com/v1"))
	require.False(t, isLoopbackBaseURL("://bad"))
	require.False(t, shouldProbe(providerSettings{Name: ProviderOpenAI}))
	require.True(t, shouldProbe(providerSettings{Name: ProviderOpenAICompatible, BaseURL: "http://localhost:8080/v1"}))
	require.False(t, shouldProbe(providerSettings{Name: ProviderOpenAICompatible, BaseURL: "https://api.example.com/v1"}))
}

func TestProviderValidationEdges(t *testing.T) {
	t.Parallel()

	_, err := resolveProviderConfig(Config{
		Provider:       ProviderOllama,
		RequestTimeout: "not-a-duration",
	}, true)
	require.ErrorContains(t, err, "parse embeddings request_timeout")

	_, err = resolveProviderConfig(Config{
		Provider:       ProviderOllama,
		RequestTimeout: "0s",
	}, true)
	require.ErrorContains(t, err, "must be positive")

	_, err = resolveProviderConfig(Config{
		Provider: ProviderOllama,
		BaseURL:  "://bad",
	}, true)
	require.ErrorContains(t, err, "invalid embeddings base_url")

	key, err := resolveAPIKey(ProviderOpenAICompatible, "MISSING_EMBED_KEY", false)
	require.NoError(t, err)
	require.Empty(t, key)

	_, err = newProvider(providerSettings{Name: "bogus"})
	require.ErrorContains(t, err, "unsupported embedding provider")

	require.Equal(t, []string{"abc"}, trimInputs([]string{"abc"}, 0))
	_, err = inferDimensions([][]float32{{}})
	require.ErrorContains(t, err, "empty vector")
	_, err = inferDimensions64([][]float64{{1}, {1, 2}})
	require.ErrorContains(t, err, "dimensions mismatch")
}

func TestOllamaProviderResponseEdges(t *testing.T) {
	t.Parallel()

	countServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		_, _ = w.Write([]byte(`{"embeddings":[]}`))
	}))
	defer countServer.Close()

	provider := newOllamaProvider(providerSettings{
		HTTPClient:    countServer.Client(),
		BaseURL:       countServer.URL,
		Model:         "fallback-model",
		MaxInputChars: 10,
	})
	_, err := provider.Embed(context.Background(), []string{"one"})
	require.ErrorContains(t, err, "returned 0 vectors for 1 inputs")

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		_, _ = w.Write([]byte(`{"embeddings":[[1,2]]}`))
	}))
	defer modelServer.Close()

	provider = newOllamaProvider(providerSettings{
		HTTPClient:    modelServer.Client(),
		BaseURL:       modelServer.URL,
		Model:         "fallback-model",
		MaxInputChars: 10,
	})
	batch, err := provider.Embed(context.Background(), []string{"one"})
	require.NoError(t, err)
	require.Equal(t, "fallback-model", batch.Model)
}

func TestCheckProviderSkipsRemoteCompatibleProbe(t *testing.T) {
	t.Parallel()

	result := CheckProvider(context.Background(), Config{
		Provider:       ProviderOpenAICompatible,
		Model:          "remote-model",
		BaseURL:        "https://api.example.com/v1",
		RequestTimeout: "5s",
	})
	require.Equal(t, "ok", result.Status)
	require.False(t, result.Probed)
	require.Empty(t, result.Warning)
}
