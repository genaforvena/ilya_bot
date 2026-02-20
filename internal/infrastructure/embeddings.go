package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const defaultEmbeddingModel = "text-embedding-ada-002"
const defaultEmbeddingBaseURL = "https://api.openai.com"

// EmbeddingClient computes text embeddings via an OpenAI-compatible API.
type EmbeddingClient struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewEmbeddingClient creates a new EmbeddingClient.
// baseURL defaults to the OpenAI API; model defaults to text-embedding-ada-002.
func NewEmbeddingClient(apiKey, baseURL, model string) *EmbeddingClient {
	if baseURL == "" {
		baseURL = defaultEmbeddingBaseURL
	}
	if model == "" {
		model = defaultEmbeddingModel
	}
	return &EmbeddingClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

type embeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for the given text.
func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	payload := embeddingRequest{Model: c.model, Input: text}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	url := c.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := make([]byte, 256)
		n, _ := resp.Body.Read(body)
		return nil, fmt.Errorf("embedding API status %d: %s", resp.StatusCode, body[:n])
	}

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("embedding API returned no data")
	}
	return embResp.Data[0].Embedding, nil
}
