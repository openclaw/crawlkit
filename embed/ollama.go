package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type ollamaProvider struct {
	client        *http.Client
	baseURL       string
	model         string
	maxInputChars int
	userAgent     string
}

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

func newOllamaProvider(settings providerSettings) Provider {
	return &ollamaProvider{
		client:        settings.HTTPClient,
		baseURL:       settings.BaseURL,
		model:         settings.Model,
		maxInputChars: settings.MaxInputChars,
		userAgent:     settings.UserAgent,
	}
}

func (p *ollamaProvider) Embed(ctx context.Context, inputs []string) (EmbeddingBatch, error) {
	if len(inputs) == 0 {
		return EmbeddingBatch{Model: p.model}, nil
	}
	payload := ollamaEmbedRequest{
		Model: p.model,
		Input: trimInputs(inputs, p.maxInputChars),
	}
	var response ollamaEmbedResponse
	if err := postJSON(ctx, p.client, p.baseURL+"/api/embed", "", p.userAgent, payload, &response); err != nil {
		return EmbeddingBatch{}, err
	}
	if len(response.Embeddings) != len(inputs) {
		return EmbeddingBatch{}, fmt.Errorf("ollama embedding response returned %d vectors for %d inputs", len(response.Embeddings), len(inputs))
	}
	dimensions, err := inferDimensions(response.Embeddings)
	if err != nil {
		return EmbeddingBatch{}, err
	}
	model := response.Model
	if model == "" {
		model = p.model
	}
	return EmbeddingBatch{Model: model, Dimensions: dimensions, Vectors: response.Embeddings, Vectors64: float32VectorsTo64(response.Embeddings)}, nil
}

func postJSON(ctx context.Context, client *http.Client, endpoint, apiKey, userAgent string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("embedding request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &HTTPError{StatusCode: resp.StatusCode, Body: string(msg), Header: resp.Header.Clone()}
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode embedding response: %w", err)
	}
	return nil
}
