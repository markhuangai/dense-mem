package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// OpenAIEmbeddingProvider implements EmbeddingProviderInterface for OpenAI-compatible APIs.
type OpenAIEmbeddingProvider struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	timeout    time.Duration
	httpClient *http.Client
	metrics    observability.DiscoverabilityMetrics
}

// Compile-time assertion that OpenAIEmbeddingProvider implements EmbeddingProviderInterface.
var _ EmbeddingProviderInterface = (*OpenAIEmbeddingProvider)(nil)

// NewOpenAIEmbeddingProvider creates a new OpenAI-compatible embedding provider.
// If httpClient is nil, a default client with the configured timeout is used.
func NewOpenAIEmbeddingProvider(cfg config.ConfigProvider, httpClient *http.Client) *OpenAIEmbeddingProvider {
	timeout := time.Duration(cfg.GetAIEmbeddingTimeoutSeconds()) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	return &OpenAIEmbeddingProvider{
		baseURL:    cfg.GetAIAPIURL(),
		apiKey:     cfg.GetAIAPIKey(),
		model:      cfg.GetAIEmbeddingModel(),
		dimensions: cfg.GetAIEmbeddingDimensions(),
		timeout:    timeout,
		httpClient: client,
		metrics:    observability.NoopDiscoverabilityMetrics(),
	}
}

// SetMetrics attaches a DiscoverabilityMetrics recorder. A nil value is
// normalised to the noop recorder so call sites need no nil checks.
// Intended for bootstrap-time wiring; not safe to call mid-request.
func (p *OpenAIEmbeddingProvider) SetMetrics(m observability.DiscoverabilityMetrics) {
	if m == nil {
		m = observability.NoopDiscoverabilityMetrics()
	}
	p.metrics = m
}

// Embed returns the embedding for a single text.
func (p *OpenAIEmbeddingProvider) Embed(ctx context.Context, text string) ([]float32, string, error) {
	vecs, model, err := p.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, "", err
	}
	if len(vecs) == 0 {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  "no embedding returned",
		}
	}
	return vecs[0], model, nil
}

// openAIEmbeddingRequest represents the request body for the OpenAI embeddings API.
type openAIEmbeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

// openAIEmbeddingResponse represents the response from the OpenAI embeddings API.
type openAIEmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage *struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// EmbedBatch returns embeddings for multiple texts in the same order as inputs.
func (p *OpenAIEmbeddingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	url := strings.TrimSuffix(p.baseURL, "/") + "/embeddings"

	reqBody := openAIEmbeddingRequest{
		Model:      p.model,
		Input:      texts,
		Dimensions: p.dimensions,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  "failed to marshal request",
			Cause:    err,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  "failed to create request",
			Cause:    err,
		}
	}

	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  "request failed",
			Cause:    err,
		}
	}
	defer resp.Body.Close()

	var respBody openAIEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  "failed to decode response",
			Cause:    err,
		}
	}

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("unexpected status code: %d", resp.StatusCode)
		if respBody.Error != nil && respBody.Error.Message != "" {
			msg = respBody.Error.Message
		}
		return nil, "", &ProviderHTTPError{
			Status:  resp.StatusCode,
			Message: msg,
		}
	}

	if len(respBody.Data) != len(texts) {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  fmt.Sprintf("expected %d embeddings, got %d", len(texts), len(respBody.Data)),
		}
	}

	if len(respBody.Data) > 0 && len(respBody.Data[0].Embedding) != p.dimensions {
		return nil, "", &ProviderError{
			Provider: "openai",
			Message:  fmt.Sprintf("expected %d dimensions, got %d", p.dimensions, len(respBody.Data[0].Embedding)),
		}
	}

	result := make([][]float32, len(respBody.Data))
	for i, d := range respBody.Data {
		result[i] = d.Embedding
	}
	if respBody.Usage != nil {
		observability.RecordEmbeddingTokens(ctx, p.metrics, p.model, respBody.Usage.PromptTokens, respBody.Usage.TotalTokens)
	}

	return result, p.model, nil
}

// ModelName returns the configured model identifier.
func (p *OpenAIEmbeddingProvider) ModelName() string {
	return p.model
}

// Dimensions returns the configured vector length.
func (p *OpenAIEmbeddingProvider) Dimensions() int {
	return p.dimensions
}

// IsAvailable returns true when the provider is configured to serve requests.
func (p *OpenAIEmbeddingProvider) IsAvailable() bool {
	return p.baseURL != "" && p.apiKey != "" && p.model != "" && p.dimensions > 0
}
