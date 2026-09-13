package embed

import (
	"context"
	"fmt"
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
	return EmbeddingBatch{Model: model, Dimensions: dimensions, Vectors: response.Embeddings, Vectors64: convertVectors[float64](response.Embeddings)}, nil
}
