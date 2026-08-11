package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	sem        chan struct{}
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
		sem:        make(chan struct{}, config.AIEmbeddingMaxConcurrency(cfg)),
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

type openAIEmbeddingUsage struct {
	PromptTokens int64 `json:"prompt_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// openAIEmbeddingResponse represents the response from the OpenAI embeddings API.
type openAIEmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage *openAIEmbeddingUsage `json:"usage"`
	Error *openAIProviderError  `json:"error"`
}

type openAIProviderError struct {
	Code string `json:"code"`
	Type string `json:"type"`
}

const nonJSONProviderErrorMessage = "provider returned a non-JSON error response"

// EmbedBatch returns embeddings for multiple texts in the same order as inputs.
func (p *OpenAIEmbeddingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}

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

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", &ProviderError{
			Provider:     "openai",
			Message:      "failed to read response",
			Cause:        err,
			FailureCode:  "provider_network_error",
			FailureClass: "transient",
		}
	}

	var respBody openAIEmbeddingResponse
	if err := json.Unmarshal(rawBody, &respBody); err != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, "", &ProviderHTTPError{
				Status:     resp.StatusCode,
				Message:    nonJSONProviderErrorMessage,
				RetryAfter: retryAfterDuration(resp.Header.Get("Retry-After")),
			}
		}
		if resp.StatusCode == http.StatusOK {
			observability.RecordAIOperationUnpriced(ctx, p.metrics, observability.AIComponentEmbedding, p.model, "missing_usage")
		}
		return nil, "", &ProviderError{
			Provider:     "openai",
			Message:      "provider response was invalid",
			Cause:        err,
			FailureCode:  "provider_response_invalid",
			FailureClass: "provider_action_required",
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", &ProviderHTTPError{
			Status:     resp.StatusCode,
			Message:    fmt.Sprintf("provider returned status %d", resp.StatusCode),
			Code:       providerErrorCode(respBody.Error),
			Type:       providerErrorType(respBody.Error),
			RetryAfter: retryAfterDuration(resp.Header.Get("Retry-After")),
		}
	}
	p.recordEmbeddingUsage(ctx, respBody.Usage, len(texts))

	if len(respBody.Data) != len(texts) {
		return nil, "", &ProviderError{
			Provider:     "openai",
			Message:      fmt.Sprintf("expected %d embeddings, got %d", len(texts), len(respBody.Data)),
			FailureCode:  "provider_response_invalid",
			FailureClass: "provider_action_required",
		}
	}

	for index, item := range respBody.Data {
		if len(item.Embedding) != p.dimensions {
			return nil, "", &ProviderError{
				Provider:     "openai",
				Message:      fmt.Sprintf("expected %d dimensions, got %d at index %d", p.dimensions, len(item.Embedding), index),
				FailureCode:  "provider_response_invalid",
				FailureClass: "provider_action_required",
			}
		}
	}

	result := make([][]float32, len(respBody.Data))
	for i, d := range respBody.Data {
		result[i] = d.Embedding
	}

	return result, p.model, nil
}

func (p *OpenAIEmbeddingProvider) recordEmbeddingUsage(ctx context.Context, usage *openAIEmbeddingUsage, itemCount int) {
	if usage == nil {
		observability.RecordAIOperationUnpriced(ctx, p.metrics, observability.AIComponentEmbedding, p.model, "missing_usage")
		return
	}
	inputTokens := usage.PromptTokens
	if inputTokens == 0 && usage.TotalTokens > 0 {
		inputTokens = usage.TotalTokens
	}
	observability.RecordEmbeddingTokens(ctx, p.metrics, p.model, usage.PromptTokens, usage.TotalTokens)
	observability.RecordAIOperationUsage(ctx, p.metrics, observability.AIOperationUsage{
		Component:   observability.AIComponentEmbedding,
		Model:       p.model,
		InputTokens: inputTokens,
		ItemCount:   itemCount,
		Source:      observability.AITokenSourceProvider,
	})
}

func providerErrorCode(err *openAIProviderError) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Code)
}

func providerErrorType(err *openAIProviderError) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Type)
}

func retryAfterDuration(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if delay := time.Until(retryAt); delay > 0 {
			return delay
		}
	}
	return 0
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
