package embed

import (
	"context"
	"fmt"
	"net/http"
)

type openAICompatibleProvider struct {
	client        *http.Client
	baseURL       string
	apiKey        string
	model         string
	maxInputChars int
	dimensions    int
	userAgent     string
}

type openAIEmbeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openAIEmbeddingResponse struct {
	Model string                `json:"model"`
	Data  []openAIEmbeddingItem `json:"data"`
}

type openAIEmbeddingItem struct {
	Index     *int      `json:"index"`
	Embedding []float64 `json:"embedding"`
}

func newOpenAICompatibleProvider(settings providerSettings) Provider {
	return &openAICompatibleProvider{
		client:        settings.HTTPClient,
		baseURL:       settings.BaseURL,
		apiKey:        settings.APIKey,
		model:         settings.Model,
		maxInputChars: settings.MaxInputChars,
		dimensions:    settings.Dimensions,
		userAgent:     settings.UserAgent,
	}
}

func (p *openAICompatibleProvider) Embed(ctx context.Context, inputs []string) (EmbeddingBatch, error) {
	if len(inputs) == 0 {
		return EmbeddingBatch{Model: p.model}, nil
	}
	payload := openAIEmbeddingRequest{
		Model:      p.model,
		Input:      trimInputs(inputs, p.maxInputChars),
		Dimensions: p.dimensions,
	}
	var response openAIEmbeddingResponse
	if err := postJSON(ctx, p.client, p.baseURL+"/embeddings", p.apiKey, p.userAgent, payload, &response); err != nil {
		return EmbeddingBatch{}, err
	}
	if len(response.Data) != len(inputs) {
		return EmbeddingBatch{}, fmt.Errorf("openai-compatible embedding response returned %d vectors for %d inputs", len(response.Data), len(inputs))
	}
	vectors := make([][]float64, len(inputs))
	seen := make([]bool, len(inputs))
	for position, item := range response.Data {
		index := position
		if item.Index != nil {
			index = *item.Index
		}
		if index < 0 || index >= len(inputs) {
			return EmbeddingBatch{}, fmt.Errorf("openai-compatible embedding response index %d out of range", index)
		}
		if seen[index] {
			return EmbeddingBatch{}, fmt.Errorf("openai-compatible embedding response duplicate index %d", index)
		}
		seen[index] = true
		vectors[index] = item.Embedding
	}
	dimensions, err := inferDimensions64(vectors)
	if err != nil {
		return EmbeddingBatch{}, err
	}
	model := response.Model
	if model == "" {
		model = p.model
	}
	return EmbeddingBatch{Model: model, Dimensions: dimensions, Vectors: float64VectorsTo32(vectors), Vectors64: vectors}, nil
}
