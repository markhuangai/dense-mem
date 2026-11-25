package structured

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	openAIVerifierProvider         = "openai"
	openAIVerifierMaxResponseBytes = modelprovider.MaxProviderDiagnosticBodyBytes
	openAIVerifierDefaultTimeout   = 60 * time.Second
)

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

// OpenAI is the shared OpenAI-compatible structured transport.
type OpenAI struct {
	baseURL, apiKey, model, tokenizer string
	httpClient                        *http.Client
	sem                               modelprovider.ConcurrencyGate
	metrics                           observability.DiscoverabilityMetrics
}

var _ modelprovider.StructuredTransport = (*OpenAI)(nil)

func NewOpenAI(cfg config.ConfigProvider, httpClient *http.Client) *OpenAI {
	return NewOpenAIWithModel(cfg, httpClient, nil, cfg.GetAIVerifierModel())
}

func NewOpenAIWithModel(cfg config.ConfigProvider, httpClient *http.Client, gate modelprovider.ConcurrencyGate, model string) *OpenAI {
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
	model = strings.TrimSpace(model)
	return &OpenAI{
		baseURL:    cfg.GetAIVerifierAPIURL(),
		apiKey:     cfg.GetAIVerifierAPIKey(),
		model:      model,
		httpClient: client,
		sem:        gate,
		metrics:    observability.NoopDiscoverabilityMetrics(),
		tokenizer:  config.AIVerifierAssessmentBudgetFor(cfg).Tokenizer,
	}
}

func (v *OpenAI) acquire(ctx context.Context) error {
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
func (v *OpenAI) SetMetrics(m observability.DiscoverabilityMetrics) {
	if m == nil {
		m = observability.NoopDiscoverabilityMetrics()
	}
	v.metrics = m
}

func (v *OpenAI) ModelName() string {
	return v.model
}

func (v *OpenAI) Complete(ctx context.Context, request modelprovider.StructuredRequest) (reply modelprovider.StructuredResult, err error) {
	model := request.Model
	statusCode := 0
	timeoutMS := v.httpClient.Timeout.Milliseconds()
	diagnostics := &modelprovider.CallDiagnostics{HTTPTimeoutMS: &timeoutMS, FailurePhase: "queue"}
	defer func() {
		if err == nil {
			diagnostics.FailurePhase = ""
		}
		reply.Diagnostics = diagnostics
		reply.StatusCode = statusCode
	}()
	queueStarted := time.Now()
	acquireErr := v.acquire(ctx)
	queueMS := time.Since(queueStarted).Milliseconds()
	diagnostics.QueueMS = &queueMS
	if acquireErr != nil {
		return modelprovider.StructuredResult{}, acquireErr
	}
	defer func() { <-v.sem }()
	diagnostics.FailurePhase = "prepare"

	started := time.Now()
	latencyOutcome := "error"
	defer func() {
		observability.RecordVerifierLatency(ctx, v.metrics, model, float64(time.Since(started).Milliseconds()), latencyOutcome)
	}()
	bodyBytes, err := modelprovider.MarshalStructuredRequest(request)
	if err != nil {
		return modelprovider.StructuredResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to marshal request",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	url := strings.TrimSuffix(v.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return modelprovider.StructuredResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "failed to create HTTP request",
			Cause:        err,
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+v.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	diagnostics.FailurePhase = "admission"
	admissionStarted := time.Now()
	admissionErr := modelprovider.NotifyAdmission(ctx)
	admissionMS := time.Since(admissionStarted).Milliseconds()
	diagnostics.AdmissionMS = &admissionMS
	if admissionErr != nil {
		return modelprovider.StructuredResult{}, admissionErr
	}

	diagnostics.FailurePhase = "request"
	httpStarted := time.Now()
	httpResp, err := v.httpClient.Do(httpReq)
	if err != nil {
		httpMS := time.Since(httpStarted).Milliseconds()
		diagnostics.HTTPMS = &httpMS
		recordOpenAIExchange(ctx, "assessor", model, bodyBytes, nil, "", 0, "no_response", started, err)
		if openAIRequestTimedOutOrCanceled(ctx, err) {
			latencyOutcome = "timeout"
			return modelprovider.StructuredResult{}, &TimeoutError{
				Provider: openAIVerifierProvider,
				Message:  "provider request timed out or was canceled",
			}
		}
		return modelprovider.StructuredResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "HTTP request failed",
			FailureClass: ProviderFailureClassTransport,
		}
	}
	statusCode = httpResp.StatusCode
	defer func() { _ = httpResp.Body.Close() }()
	diagnostics.FailurePhase = "response_body"
	rawResponse, readErr := io.ReadAll(io.LimitReader(httpResp.Body, openAIVerifierMaxResponseBytes+1))
	httpMS := time.Since(httpStarted).Milliseconds()
	diagnostics.HTTPMS = &httpMS
	if readErr != nil {
		recordOpenAIExchange(ctx, "assessor", model, bodyBytes, rawResponse, httpResp.Header.Get("Content-Type"), httpResp.StatusCode, "response_read_failed", started, readErr)
		if openAIRequestTimedOutOrCanceled(ctx, readErr) {
			latencyOutcome = "timeout"
			return modelprovider.StructuredResult{}, &TimeoutError{Provider: openAIVerifierProvider, Message: "provider response timed out or was canceled"}
		}
		return modelprovider.StructuredResult{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to read provider response", FailureClass: ProviderFailureClassProtocol}
	}
	if len(rawResponse) > openAIVerifierMaxResponseBytes {
		recordOpenAIExchange(ctx, "assessor", model, bodyBytes, rawResponse, httpResp.Header.Get("Content-Type"), httpResp.StatusCode, "response_too_large", started, nil)
		return modelprovider.StructuredResult{}, &ProviderError{Provider: openAIVerifierProvider, Message: fmt.Sprintf("provider response exceeds %d byte transport limit", openAIVerifierMaxResponseBytes), FailureClass: ProviderFailureClassProtocol}
	}
	recordOpenAIExchange(ctx, "assessor", model, bodyBytes, rawResponse, httpResp.Header.Get("Content-Type"), httpResp.StatusCode, "captured", started, nil)

	diagnostics.FailurePhase = "response_status"
	if httpResp.StatusCode == http.StatusTooManyRequests {
		latencyOutcome = "rate_limited"
		return modelprovider.StructuredResult{}, &RateLimitError{
			Provider:   openAIVerifierProvider,
			Message:    "provider returned HTTP 429",
			RetryAfter: modelprovider.RetryAfterSeconds(httpResp.Header.Get("Retry-After"), time.Now()),
		}
	}
	if httpResp.StatusCode != http.StatusOK {
		latencyOutcome = "provider_error"
		return modelprovider.StructuredResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      fmt.Sprintf("provider returned HTTP %d", httpResp.StatusCode),
			FailureClass: openAIHTTPFailureClass(httpResp.StatusCode),
			StatusCode:   httpResp.StatusCode,
		}
	}

	diagnostics.FailurePhase = "response_decode"
	apiResp, err := decodeOpenAIVerifierAPIResponse(bytes.NewReader(rawResponse))
	if err != nil {
		if httpResp.StatusCode == http.StatusOK {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "provider_error"
		return modelprovider.StructuredResult{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid provider response envelope",
			FailureClass: ProviderFailureClassProtocol,
		}
	}
	reported := apiResp.Usage
	result := modelprovider.StructuredResult{}
	if reported != nil {
		result.UsageAvailable = true
		result.PromptTokens = int(reported.PromptTokens)
		result.CompletionTokens = int(reported.CompletionTokens)
		result.TotalTokens = int(reported.TotalTokens)
	}
	present := openAIVerifierUsageSupportsPricing(reported)
	v.recordVerifierProviderUsage(ctx, model, reported)
	if len(apiResp.Choices) == 0 {
		if !present {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "provider_error"
		return result, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "provider response contained no choices",
			FailureClass: ProviderFailureClassProtocol,
		}
	}
	content := apiResp.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		if !present {
			v.recordVerifierMissingUsage(ctx, model)
		}
		latencyOutcome = "provider_error"
		return result, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "provider response contained empty assistant content",
			FailureClass: ProviderFailureClassProtocol,
		}
	}
	if !present {
		v.recordVerifierTokenizerUsage(ctx, model, bodyBytes, content)
	}
	latencyOutcome = "ok"
	result.Content = content
	return result, nil
}

func recordOpenAIExchange(ctx context.Context, component, model string, requestBody, responseBody []byte, responseContentType string, statusCode int, outcome string, started time.Time, transportErr error) {
	recorder := modelprovider.ExchangeRecorderFromContext(ctx)
	if recorder == nil {
		return
	}
	captureState := ""
	if len(requestBody) > modelprovider.MaxProviderDiagnosticBodyBytes || len(responseBody) > modelprovider.MaxProviderDiagnosticBodyBytes {
		captureState = "truncated"
	}
	if failure := modelprovider.TransientNetworkFailure(transportErr); failure != "" {
		if outcome == "no_response" {
			captureState = "no_response"
		} else if captureState == "" {
			captureState = "interrupted"
		}
		outcome = failure
	}
	recorder.RecordProviderExchange(ctx, modelprovider.ProviderExchange{
		Component:           component,
		Model:               model,
		RequestBody:         append([]byte(nil), requestBody...),
		ResponseBody:        append([]byte(nil), responseBody...),
		RequestContentType:  "application/json",
		ResponseContentType: responseContentType,
		StatusCode:          statusCode,
		Outcome:             outcome,
		CaptureState:        captureState,
		StartedAt:           started,
		CompletedAt:         time.Now(),
	})
}

func openAIHTTPFailureClass(statusCode int) string {
	switch {
	case statusCode >= 400 && statusCode < 500:
		return ProviderFailureClassHTTPClient
	case statusCode >= 500 && statusCode < 600:
		return ProviderFailureClassHTTPServer
	default:
		return ProviderFailureClassHTTPUnexpected
	}
}

func openAIRequestTimedOutOrCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

type ProviderError = modelprovider.ProviderError
type TimeoutError = modelprovider.TimeoutError
type RateLimitError = modelprovider.RateLimitError

const (
	ProviderFailureClassHTTPClient          = modelprovider.ProviderFailureClassHTTPClient
	ProviderFailureClassHTTPServer          = modelprovider.ProviderFailureClassHTTPServer
	ProviderFailureClassHTTPUnexpected      = modelprovider.ProviderFailureClassHTTPUnexpected
	ProviderFailureClassTransport           = modelprovider.ProviderFailureClassTransport
	ProviderFailureClassProtocol            = modelprovider.ProviderFailureClassProtocol
	ProviderFailureClassRequestInvalid      = modelprovider.ProviderFailureClassRequestInvalid
	ProviderFailureClassProviderUnavailable = modelprovider.ProviderFailureClassProviderUnavailable
)
