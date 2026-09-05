package assessorprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	openAIVerifierProvider         = "openai"
	openAIVerifierMaxItemChars     = 4000
	openAIVerifierMaxTotalChars    = 32000
	openAIVerifierMaxResponseBytes = 16 << 20
	openAIVerifierDefaultTimeout   = 60 * time.Second
)

// openAIVerifierMessage is a single chat message.
type openAIVerifierMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIVerifierJSONSchema wraps the schema with metadata for the response_format field.
type openAIVerifierJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// openAIVerifierResponseFormat selects structured JSON output mode.
type openAIVerifierResponseFormat struct {
	Type       string                   `json:"type"`
	JSONSchema openAIVerifierJSONSchema `json:"json_schema"`
}

// openAIVerifierRequest is the request body sent to /chat/completions.
type openAIVerifierRequest struct {
	Model          string                       `json:"model"`
	Messages       []openAIVerifierMessage      `json:"messages"`
	Temperature    *float64                     `json:"temperature,omitempty"`
	ResponseFormat openAIVerifierResponseFormat `json:"response_format"`
}

// openAIVerifierAPIResponse represents the outer chat completions response envelope.
type openAIVerifierUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type openAIVerifierAPIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *openAIVerifierUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func decodeOpenAIVerifierAPIResponse(body io.Reader) (openAIVerifierAPIResponse, error) {
	data, err := io.ReadAll(io.LimitReader(body, openAIVerifierMaxResponseBytes+1))
	if err != nil {
		return openAIVerifierAPIResponse{}, err
	}
	if len(data) > openAIVerifierMaxResponseBytes {
		return openAIVerifierAPIResponse{}, fmt.Errorf("provider response exceeds %d byte transport limit", openAIVerifierMaxResponseBytes)
	}
	var response openAIVerifierAPIResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return openAIVerifierAPIResponse{}, err
	}
	return response, nil
}

// OpenAIAssessor implements Verifier for OpenAI-compatible chat APIs.
// It is safe for concurrent use: all fields are set during construction and
// never mutated thereafter.
type OpenAIAssessor struct {
	baseURL            string
	apiKey             string
	model              string
	disableTemperature bool
	httpClient         *http.Client
	sem                chan struct{}
	metrics            observability.DiscoverabilityMetrics
	assessmentLimits   assessor.SemanticAssessmentLimits
}

type Provider = assessor.Provider

var _ modelprovider.StructuredTransport = (*OpenAIAssessor)(nil)

// Complete exposes the shared structured-output transport seam for future
// capability adapters while Remember keeps its validation in this package.
func (v *OpenAIAssessor) Complete(
	ctx context.Context,
	request modelprovider.StructuredRequest,
) (modelprovider.StructuredResult, error) {
	messages := make([]openAIVerifierMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, openAIVerifierMessage{Role: message.Role, Content: message.Content})
	}
	result, err := v.openAIStructuredChatMessagesJSONWithUsage(
		ctx, request.Model, request.SchemaName, request.Schema, messages,
	)
	if err != nil {
		return modelprovider.StructuredResult{}, err
	}
	structured := modelprovider.StructuredResult{Content: result.Content}
	if result.ReportedUsage != nil {
		structured.PromptTokens = int(result.ReportedUsage.PromptTokens)
		structured.CompletionTokens = int(result.ReportedUsage.CompletionTokens)
		structured.TotalTokens = int(result.ReportedUsage.TotalTokens)
	}
	return structured, nil
}

// NewOpenAIAssessor creates a new OpenAI-compatible assessor using the supplied
// configuration. If httpClient is nil a default client with a 60-second timeout
// is used.
func NewOpenAIAssessor(cfg config.ConfigProvider, httpClient *http.Client) *OpenAIAssessor {
	return NewOpenAIAssessorWithAssessmentLimits(cfg, httpClient, SemanticAssessmentLimitsForConfig(cfg))
}

// NewOpenAIAssessorWithAssessmentLimits creates an assessor with the supplied
// shared assessor limits.
func NewOpenAIAssessorWithAssessmentLimits(
	cfg config.ConfigProvider,
	httpClient *http.Client,
	assessmentLimits assessor.SemanticAssessmentLimits,
) *OpenAIAssessor {
	return NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGate(cfg, httpClient, assessmentLimits, nil)
}

// NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGate creates an assessor
// using the supplied process-wide outbound request gate.
func NewOpenAIAssessorWithAssessmentLimitsAndConcurrencyGate(
	cfg config.ConfigProvider,
	httpClient *http.Client,
	assessmentLimits assessor.SemanticAssessmentLimits,
	gate modelprovider.ConcurrencyGate,
) *OpenAIAssessor {
	client := httpClient
	if client == nil {
		timeout := time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second
		if timeout == 0 {
			timeout = openAIVerifierDefaultTimeout
		}
		client = &http.Client{Timeout: timeout}
	}

	if gate == nil {
		gate = modelprovider.NewConcurrencyGate(config.AIVerifierMaxConcurrency(cfg))
	}
	return &OpenAIAssessor{
		baseURL:            cfg.GetAIVerifierAPIURL(),
		apiKey:             cfg.GetAIVerifierAPIKey(),
		model:              cfg.GetAIVerifierModel(),
		disableTemperature: config.AIVerifierTemperatureDisabled(cfg),
		httpClient:         client,
		sem:                gate,
		metrics:            observability.NoopDiscoverabilityMetrics(),
		assessmentLimits:   assessor.NormalizeSemanticAssessmentLimits(assessmentLimits),
	}
}

// SemanticAssessmentLimitsForConfig maps the configured assessor budget into the
// single limits value shared by the provider and placement worker.
func SemanticAssessmentLimitsForConfig(cfg config.ConfigProvider) assessor.SemanticAssessmentLimits {
	budget := config.AIVerifierAssessmentBudgetFor(cfg)
	limits := DefaultSemanticAssessmentLimits()
	limits.Tokenizer = budget.Tokenizer
	limits.MaxInputTokens = budget.MaxInputTokens
	limits.MaxOutputTokens = budget.MaxOutputTokens
	limits.MaxCandidateContextTokens = budget.MaxCandidateContextTokens
	limits.MaxPredicateOptions = budget.MaxPredicateOptions
	return limits
}

func DefaultSemanticAssessmentLimits() assessor.SemanticAssessmentLimits {
	return assessor.DefaultSemanticAssessmentLimits()
}

func (v *OpenAIAssessor) acquire(ctx context.Context) error {
	if err := modelprovider.AcquireConcurrency(ctx, v.sem); err != nil {
		return &TimeoutError{
			Provider: openAIVerifierProvider,
			Message:  err.Error(),
		}
	}
	return nil
}

// SetMetrics attaches a DiscoverabilityMetrics recorder. A nil value is
// normalised to the noop recorder so call sites need no nil checks.
// Intended for bootstrap-time wiring; not safe to call mid-request.
func (v *OpenAIAssessor) SetMetrics(m observability.DiscoverabilityMetrics) {
	if m == nil {
		m = observability.NoopDiscoverabilityMetrics()
	}
	v.metrics = m
}

func (v *OpenAIAssessor) ModelName() string {
	return v.model
}

func (v *OpenAIAssessor) openAIStructuredChatJSON(
	ctx context.Context,
	model string,
	schemaName string,
	schema map[string]any,
	systemPrompt string,
	payload any,
) (string, error) {
	if err := v.acquire(ctx); err != nil {
		return "", err
	}
	defer func() { <-v.sem }()

	started := time.Now()
	latencyOutcome := "error"
	defer func() {
		observability.RecordVerifierLatency(ctx, v.metrics, model, float64(time.Since(started).Milliseconds()), latencyOutcome)
	}()
	schemaRaw, err := json.Marshal(schema)
	if err != nil {
		return "", &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to marshal structured schema",
			Cause:    err,
		}
	}
	userJSON, err := json.Marshal(payload)
	if err != nil {
		return "", &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to marshal user payload",
			Cause:    err,
		}
	}
	chatReq := openAIVerifierRequest{
		Model: model,
		Messages: []openAIVerifierMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature: openAIVerifierTemperature(v.disableTemperature),
		ResponseFormat: openAIVerifierResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIVerifierJSONSchema{
				Name:   schemaName,
				Strict: true,
				Schema: schemaRaw,
			},
		},
	}
	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return "", &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to marshal request",
			Cause:    err,
		}
	}
	url := strings.TrimSuffix(v.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to create HTTP request",
			Cause:    err,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+v.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if err := modelprovider.NotifyAdmission(ctx); err != nil {
		return "", err
	}

	httpResp, err := v.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			latencyOutcome = "timeout"
			return "", &TimeoutError{
				Provider: openAIVerifierProvider,
				Message:  ctx.Err().Error(),
			}
		}
		return "", &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "HTTP request failed",
			Cause:    err,
		}
	}
	defer func() { _ = httpResp.Body.Close() }()

	apiResp, err := decodeOpenAIVerifierAPIResponse(httpResp.Body)
	if err != nil {
		if httpResp.StatusCode == http.StatusOK {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "malformed"
		return "", &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "failed to decode API response",
		}
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		latencyOutcome = "rate_limited"
		return "", &RateLimitError{
			Provider: openAIVerifierProvider,
			Message:  "provider returned HTTP 429",
		}
	}
	if httpResp.StatusCode != http.StatusOK {
		return "", &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      fmt.Sprintf("provider returned HTTP %d", httpResp.StatusCode),
			FailureClass: openAIHTTPFailureClass(httpResp.StatusCode),
			StatusCode:   httpResp.StatusCode,
		}
	}
	v.recordVerifierProviderUsage(ctx, model, apiResp.Usage)
	if len(apiResp.Choices) == 0 {
		if !openAIVerifierUsageSupportsPricing(apiResp.Usage) {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "malformed"
		return "", &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "no choices in response",
		}
	}
	content := apiResp.Choices[0].Message.Content
	if !openAIVerifierUsageSupportsPricing(apiResp.Usage) {
		if strings.TrimSpace(content) == "" {
			v.recordVerifierMissingUsage(ctx, model)
		} else {
			v.recordVerifierTokenizerUsage(ctx, model, chatReq, content)
		}
	}
	latencyOutcome = "ok"
	return content, nil
}

func (v *OpenAIAssessor) openAIStructuredChatJSONWithUsage(
	ctx context.Context,
	model string,
	schemaName string,
	schema map[string]any,
	systemPrompt string,
	payload any,
) (openAIStructuredChatResult, error) {
	userJSON, err := json.Marshal(payload)
	if err != nil {
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to marshal user payload",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	return v.openAIStructuredChatMessagesJSONWithUsage(
		ctx,
		model,
		schemaName,
		schema,
		[]openAIVerifierMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
	)
}

func (v *OpenAIAssessor) openAIStructuredChatMessagesJSONWithUsage(
	ctx context.Context,
	model string,
	schemaName string,
	schema map[string]any,
	messages []openAIVerifierMessage,
) (openAIStructuredChatResult, error) {
	if err := v.acquire(ctx); err != nil {
		return openAIStructuredChatResult{}, err
	}
	defer func() { <-v.sem }()

	started := time.Now()
	latencyOutcome := "error"
	defer func() {
		observability.RecordVerifierLatency(ctx, v.metrics, model, float64(time.Since(started).Milliseconds()), latencyOutcome)
	}()
	schemaRaw, err := json.Marshal(schema)
	if err != nil {
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to marshal structured schema",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	chatReq := openAIVerifierRequest{
		Model:       model,
		Messages:    append([]openAIVerifierMessage(nil), messages...),
		Temperature: openAIVerifierTemperature(v.disableTemperature),
		ResponseFormat: openAIVerifierResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIVerifierJSONSchema{
				Name:   schemaName,
				Strict: true,
				Schema: schemaRaw,
			},
		},
	}
	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to marshal request",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	url := strings.TrimSuffix(v.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to create HTTP request",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+v.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if err := modelprovider.NotifyAdmission(ctx); err != nil {
		return openAIStructuredChatResult{}, err
	}

	httpResp, err := v.httpClient.Do(httpReq)
	if err != nil {
		if openAIRequestTimedOutOrCanceled(ctx, err) {
			latencyOutcome = "timeout"
			return openAIStructuredChatResult{}, &TimeoutError{
				Provider: openAIVerifierProvider,
				Message:  "provider request timed out or was canceled",
			}
		}
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "HTTP request failed",
			FailureClass: ProviderFailureClassTransport,
		}
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusTooManyRequests {
		latencyOutcome = "rate_limited"
		return openAIStructuredChatResult{}, &RateLimitError{
			Provider:   openAIVerifierProvider,
			Message:    "provider returned HTTP 429",
			RetryAfter: openAIRetryAfterSeconds(httpResp.Header.Get("Retry-After"), time.Now()),
		}
	}
	if httpResp.StatusCode != http.StatusOK {
		latencyOutcome = "provider_error"
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      fmt.Sprintf("provider returned HTTP %d", httpResp.StatusCode),
			FailureClass: openAIHTTPFailureClass(httpResp.StatusCode),
			StatusCode:   httpResp.StatusCode,
		}
	}

	apiResp, err := decodeOpenAIVerifierAPIResponse(httpResp.Body)
	if err != nil {
		if httpResp.StatusCode == http.StatusOK {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "provider_error"
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid provider response envelope",
			FailureClass: ProviderFailureClassProtocol,
		}
	}
	reportedUsage := apiResp.Usage
	usage := reportedUsage
	if !openAIVerifierUsageSupportsPricing(usage) {
		usage = nil
	}
	v.recordVerifierProviderUsage(ctx, model, reportedUsage)
	if len(apiResp.Choices) == 0 {
		if usage == nil {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "provider_error"
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "provider response contained no choices",
			FailureClass: ProviderFailureClassProtocol,
		}
	}
	content := apiResp.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		if usage == nil {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "provider_error"
		return openAIStructuredChatResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "provider response contained empty assistant content",
			FailureClass: ProviderFailureClassProtocol,
		}
	}
	if usage == nil {
		v.recordVerifierTokenizerUsage(ctx, model, chatReq, content)
	}
	latencyOutcome = "ok"
	return openAIStructuredChatResult{
		Content:       content,
		Usage:         usage,
		ReportedUsage: reportedUsage,
	}, nil
}

func openAIVerifierTemperature(disabled bool) *float64 {
	if disabled {
		return nil
	}
	temperature := 0.0
	return &temperature
}

const providerFailureMaxRetryAfter = 5 * time.Minute

type ProviderError = modelprovider.ProviderError
type TimeoutError = modelprovider.TimeoutError
type RateLimitError = modelprovider.RateLimitError
type MalformedResponseError = modelprovider.MalformedResponseError
type FailureMeasurement = modelprovider.FailureMeasurement

const (
	ProviderFailureClassHTTPClient          = modelprovider.ProviderFailureClassHTTPClient
	ProviderFailureClassHTTPServer          = modelprovider.ProviderFailureClassHTTPServer
	ProviderFailureClassHTTPUnexpected      = modelprovider.ProviderFailureClassHTTPUnexpected
	ProviderFailureClassTransport           = modelprovider.ProviderFailureClassTransport
	ProviderFailureClassProtocol            = modelprovider.ProviderFailureClassProtocol
	ProviderFailureClassRequestInvalid      = modelprovider.ProviderFailureClassRequestInvalid
	ProviderFailureClassProviderUnavailable = modelprovider.ProviderFailureClassProviderUnavailable
)
