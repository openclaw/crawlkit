package embed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	ProviderOpenAI           = "openai"
	ProviderOllama           = "ollama"
	ProviderLlamaCpp         = "llamacpp"
	ProviderOpenAICompatible = "openai_compatible"

	DefaultOpenAIBaseURL       = "https://api.openai.com/v1"
	DefaultOllamaBaseURL       = "http://127.0.0.1:11434"
	DefaultLlamaCppBaseURL     = "http://127.0.0.1:8080/v1"
	DefaultOpenAIModel         = "text-embedding-3-small"
	DefaultLocalEmbeddingModel = "nomic-embed-text"
	DefaultBatchSize           = 64
	DefaultMaxInputChars       = 12000
	DefaultRequestTimeout      = 2 * time.Minute
	DefaultProbeTimeout        = 2 * time.Second
)

type Config struct {
	Provider       string
	Model          string
	BaseURL        string
	APIKeyEnv      string
	RequestTimeout string
	MaxInputChars  int
	Dimensions     int
	UserAgent      string
}

type Provider interface {
	Embed(ctx context.Context, inputs []string) (EmbeddingBatch, error)
}

type EmbeddingBatch struct {
	Model      string
	Dimensions int
	Vectors    [][]float32
	Vectors64  [][]float64
}

type HTTPError struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("embedding request failed with HTTP %d: %s", e.StatusCode, e.Body)
}

func IsRateLimitError(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests
}

type CheckResult struct {
	Provider string
	Model    string
	BaseURL  string
	Status   string
	Warning  string
	Probed   bool
}

type Option func(*providerOptions)

type providerOptions struct {
	httpClient      *http.Client
	timeoutOverride time.Duration
	apiKeyOverride  *string
	userAgent       string
}

type providerSettings struct {
	Name          string
	Model         string
	BaseURL       string
	APIKey        string
	MaxInputChars int
	Dimensions    int
	UserAgent     string
	Timeout       time.Duration
	HTTPClient    *http.Client
}

func WithHTTPClient(client *http.Client) Option {
	return func(opts *providerOptions) {
		opts.httpClient = client
	}
}

func WithRequestTimeout(timeout time.Duration) Option {
	return func(opts *providerOptions) {
		opts.timeoutOverride = timeout
	}
}

func WithAPIKey(apiKey string) Option {
	return func(opts *providerOptions) {
		value := strings.TrimSpace(apiKey)
		opts.apiKeyOverride = &value
	}
}

func WithUserAgent(userAgent string) Option {
	return func(opts *providerOptions) {
		opts.userAgent = strings.TrimSpace(userAgent)
	}
}

func NewProvider(cfg Config, opts ...Option) (Provider, error) {
	settings, err := resolveProviderConfig(cfg, true, opts...)
	if err != nil {
		return nil, err
	}
	return newProvider(settings)
}

func CheckProvider(ctx context.Context, cfg Config) CheckResult {
	settings, err := resolveProviderConfig(cfg, true, WithRequestTimeout(DefaultProbeTimeout))
	if err != nil {
		return CheckResult{
			Provider: normalizedProviderName(cfg.Provider),
			Model:    strings.TrimSpace(cfg.Model),
			BaseURL:  strings.TrimSpace(cfg.BaseURL),
			Status:   "warning",
			Warning:  err.Error(),
		}
	}
	result := CheckResult{
		Provider: settings.Name,
		Model:    settings.Model,
		BaseURL:  settings.BaseURL,
		Status:   "ok",
	}
	if !shouldProbe(settings) {
		return result
	}
	provider, err := newProvider(settings)
	if err != nil {
		result.Status = "warning"
		result.Warning = err.Error()
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()
	if _, err := provider.Embed(probeCtx, []string{"crawlkit probe"}); err != nil {
		result.Status = "warning"
		result.Warning = err.Error()
		return result
	}
	result.Probed = true
	return result
}

func resolveProviderConfig(cfg Config, validateAPIKey bool, opts ...Option) (providerSettings, error) {
	options := providerOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	name := normalizedProviderName(cfg.Provider)
	if name == "" {
		name = ProviderOpenAI
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultModel(name)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		switch name {
		case ProviderOpenAI:
			baseURL = DefaultOpenAIBaseURL
		case ProviderOllama:
			baseURL = DefaultOllamaBaseURL
		case ProviderLlamaCpp:
			baseURL = DefaultLlamaCppBaseURL
		case ProviderOpenAICompatible:
			return providerSettings{}, fmt.Errorf("embedding provider %q requires base_url", name)
		}
	}
	timeout := DefaultRequestTimeout
	if strings.TrimSpace(cfg.RequestTimeout) != "" {
		parsed, err := time.ParseDuration(cfg.RequestTimeout)
		if err != nil {
			return providerSettings{}, fmt.Errorf("parse embeddings request_timeout: %w", err)
		}
		if parsed <= 0 {
			return providerSettings{}, errors.New("embeddings request_timeout must be positive")
		}
		timeout = parsed
	}
	if options.timeoutOverride > 0 && options.timeoutOverride < timeout {
		timeout = options.timeoutOverride
	}
	maxInputChars := cfg.MaxInputChars
	if maxInputChars <= 0 {
		maxInputChars = DefaultMaxInputChars
	}
	switch name {
	case ProviderOpenAI, ProviderOllama, ProviderLlamaCpp, ProviderOpenAICompatible:
	default:
		return providerSettings{}, fmt.Errorf("unsupported embedding provider %q", name)
	}
	apiKey := ""
	if options.apiKeyOverride != nil {
		apiKey = *options.apiKeyOverride
	} else {
		var err error
		apiKey, err = resolveAPIKey(name, cfg.APIKeyEnv, validateAPIKey)
		if err != nil {
			return providerSettings{}, err
		}
	}
	client := options.httpClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return providerSettings{}, fmt.Errorf("invalid embeddings base_url %q: %w", baseURL, err)
	}
	return providerSettings{
		Name:          name,
		Model:         model,
		BaseURL:       baseURL,
		APIKey:        apiKey,
		MaxInputChars: maxInputChars,
		Dimensions:    cfg.Dimensions,
		UserAgent:     firstNonEmpty(options.userAgent, cfg.UserAgent),
		Timeout:       timeout,
		HTTPClient:    client,
	}, nil
}

func newProvider(settings providerSettings) (Provider, error) {
	switch settings.Name {
	case ProviderOllama:
		return newOllamaProvider(settings), nil
	case ProviderOpenAI, ProviderLlamaCpp, ProviderOpenAICompatible:
		return newOpenAICompatibleProvider(settings), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", settings.Name)
	}
}

func resolveAPIKey(provider, apiKeyEnv string, validate bool) (string, error) {
	envName := strings.TrimSpace(apiKeyEnv)
	required := provider == ProviderOpenAI
	if envName == "" {
		if required {
			envName = "OPENAI_API_KEY"
		} else {
			return "", nil
		}
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		if required || validate {
			return "", fmt.Errorf("embedding provider %q requires API key env %s", provider, envName)
		}
		return "", nil
	}
	return value, nil
}

func normalizedProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func defaultModel(provider string) string {
	switch provider {
	case ProviderOllama, ProviderLlamaCpp:
		return DefaultLocalEmbeddingModel
	default:
		return DefaultOpenAIModel
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func shouldProbe(settings providerSettings) bool {
	switch settings.Name {
	case ProviderOllama, ProviderLlamaCpp:
		return true
	case ProviderOpenAICompatible:
		return isLoopbackBaseURL(settings.BaseURL)
	default:
		return false
	}
}

func isLoopbackBaseURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trimInputs(inputs []string, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = DefaultMaxInputChars
	}
	out := make([]string, len(inputs))
	for i, input := range inputs {
		runes := []rune(input)
		if len(runes) > maxChars {
			runes = runes[:maxChars]
		}
		out[i] = string(runes)
	}
	return out
}

func inferDimensions(vectors [][]float32) (int, error) {
	dimensions := 0
	for _, vector := range vectors {
		if len(vector) == 0 {
			return 0, errors.New("embedding response contained an empty vector")
		}
		if dimensions == 0 {
			dimensions = len(vector)
			continue
		}
		if len(vector) != dimensions {
			return 0, fmt.Errorf("embedding response dimensions mismatch: got %d want %d", len(vector), dimensions)
		}
	}
	return dimensions, nil
}

func inferDimensions64(vectors [][]float64) (int, error) {
	dimensions := 0
	for _, vector := range vectors {
		if len(vector) == 0 {
			return 0, errors.New("embedding response contained an empty vector")
		}
		if dimensions == 0 {
			dimensions = len(vector)
			continue
		}
		if len(vector) != dimensions {
			return 0, fmt.Errorf("embedding response dimensions mismatch: got %d want %d", len(vector), dimensions)
		}
	}
	return dimensions, nil
}

func float32VectorsTo64(vectors [][]float32) [][]float64 {
	out := make([][]float64, len(vectors))
	for i, vector := range vectors {
		out[i] = make([]float64, len(vector))
		for j, value := range vector {
			out[i][j] = float64(value)
		}
	}
	return out
}

func float64VectorsTo32(vectors [][]float64) [][]float32 {
	out := make([][]float32, len(vectors))
	for i, vector := range vectors {
		out[i] = make([]float32, len(vector))
		for j, value := range vector {
			out[i][j] = float32(value)
		}
	}
	return out
}
