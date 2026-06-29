package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	openAIVerifierProvider       = "openai"
	openAIVerifierMaxItemChars   = 4000
	openAIVerifierMaxTotalChars  = 32000
	openAIVerifierDefaultTimeout = 60 * time.Second

	// openAIVerifierSystemPrompt is the fixed system instruction for all verification calls.
	// Temperature is set to 0 when included, and a strict JSON schema is enforced.
	openAIVerifierSystemPrompt = `You are a fact-verification assistant. Given a claim, optional temporal validity bounds, and a list of evidence items, determine whether the evidence supports ("entailed"), contradicts ("contradicted"), or is insufficient to assess ("insufficient") the claim within the stated temporal scope.

If valid_from or valid_to is provided, evaluate the claim for that time-bounded scope. Later or earlier evidence outside that scope should not contradict the claim unless it directly says the claim was false inside the scope.

Respond ONLY with a JSON object conforming to the required schema:
- "verdict": exactly one of "entailed", "contradicted", or "insufficient"
- "confidence": a float in [0.0, 1.0] expressing your confidence in the verdict
- "rationale": a concise, non-empty explanation of your reasoning`
)

// verifierResponseSchema is the strict JSON schema enforced via response_format.
// It is declared as a package-level variable so it is parsed once and reused.
var verifierResponseSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"verdict":    {"type": "string", "enum": ["entailed", "contradicted", "insufficient"]},
		"confidence": {"type": "number"},
		"rationale":  {"type": "string"}
	},
	"required": ["verdict", "confidence", "rationale"],
	"additionalProperties": false
}`)

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
type openAIVerifierAPIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// openAIVerifierResult is the structured payload the LLM returns inside the content field.
type openAIVerifierResult struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

// OpenAIVerifier implements Verifier for OpenAI-compatible chat APIs.
// It is safe for concurrent use: all fields are set during construction and
// never mutated thereafter.
type OpenAIVerifier struct {
	baseURL            string
	apiKey             string
	model              string
	disableTemperature bool
	httpClient         *http.Client
	metrics            observability.DiscoverabilityMetrics
}

// Compile-time assertion that OpenAIVerifier implements Verifier.
var _ Verifier = (*OpenAIVerifier)(nil)

// NewOpenAIVerifier creates a new OpenAI-compatible verifier using the supplied
// configuration. If httpClient is nil a default client with a 60-second timeout
// is used.
func NewOpenAIVerifier(cfg config.ConfigProvider, httpClient *http.Client) *OpenAIVerifier {
	client := httpClient
	if client == nil {
		timeout := time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second
		if timeout == 0 {
			timeout = openAIVerifierDefaultTimeout
		}
		client = &http.Client{Timeout: timeout}
	}

	return &OpenAIVerifier{
		baseURL:            cfg.GetAIVerifierAPIURL(),
		apiKey:             cfg.GetAIVerifierAPIKey(),
		model:              cfg.GetAIVerifierModel(),
		disableTemperature: config.AIVerifierTemperatureDisabled(cfg),
		httpClient:         client,
		metrics:            observability.NoopDiscoverabilityMetrics(),
	}
}

// SetMetrics attaches a DiscoverabilityMetrics recorder. A nil value is
// normalised to the noop recorder so call sites need no nil checks.
// Intended for bootstrap-time wiring; not safe to call mid-request.
func (v *OpenAIVerifier) SetMetrics(m observability.DiscoverabilityMetrics) {
	if m == nil {
		m = observability.NoopDiscoverabilityMetrics()
	}
	v.metrics = m
}

// Verify submits req to the OpenAI-compatible chat completions endpoint and
// returns a structured Response. The returned error is one of the sentinel
// types defined in errors.go (ErrVerifierTimeout, ErrVerifierProvider,
// ErrVerifierRateLimit, ErrVerifierMalformedResponse).
func (v *OpenAIVerifier) Verify(ctx context.Context, req Request) (Response, error) {
	started := time.Now()
	latencyOutcome := "error"
	defer func() {
		observability.RecordVerifierLatency(ctx, v.metrics, v.model, float64(time.Since(started).Milliseconds()), latencyOutcome)
	}()

	evidence := prepareEvidence(req.Context)

	// Build the user payload as JSON so the LLM receives a machine-readable object.
	type userPayload struct {
		Claim     string   `json:"claim"`
		ValidFrom string   `json:"valid_from,omitempty"`
		ValidTo   string   `json:"valid_to,omitempty"`
		Evidence  []string `json:"evidence"`
	}
	payload := userPayload{
		Claim:    req.Predicate,
		Evidence: evidence,
	}
	if req.ValidFrom != nil && !req.ValidFrom.IsZero() {
		payload.ValidFrom = req.ValidFrom.UTC().Format(time.RFC3339)
	}
	if req.ValidTo != nil && !req.ValidTo.IsZero() {
		payload.ValidTo = req.ValidTo.UTC().Format(time.RFC3339)
	}

	userJSON, err := json.Marshal(payload)
	if err != nil {
		return Response{}, &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to marshal user payload",
			Cause:    err,
		}
	}

	chatReq := openAIVerifierRequest{
		Model: v.model,
		Messages: []openAIVerifierMessage{
			{Role: "system", Content: openAIVerifierSystemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature: openAIVerifierTemperature(v.disableTemperature),
		ResponseFormat: openAIVerifierResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIVerifierJSONSchema{
				Name:   "verification_result",
				Strict: true,
				Schema: verifierResponseSchema,
			},
		},
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return Response{}, &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to marshal request",
			Cause:    err,
		}
	}

	url := strings.TrimSuffix(v.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return Response{}, &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "failed to create HTTP request",
			Cause:    err,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+v.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := v.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			latencyOutcome = "timeout"
			return Response{}, &TimeoutError{
				Provider: openAIVerifierProvider,
				Message:  ctx.Err().Error(),
			}
		}
		return Response{}, &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  "HTTP request failed",
			Cause:    err,
		}
	}
	defer httpResp.Body.Close()

	var apiResp openAIVerifierAPIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&apiResp); err != nil {
		latencyOutcome = "malformed"
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "failed to decode API response",
		}
	}

	if httpResp.StatusCode == http.StatusTooManyRequests {
		msg := "rate limited"
		if apiResp.Error != nil && apiResp.Error.Message != "" {
			msg = apiResp.Error.Message
		}
		latencyOutcome = "rate_limited"
		return Response{}, &RateLimitError{
			Provider: openAIVerifierProvider,
			Message:  msg,
		}
	}

	if httpResp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("unexpected status %d", httpResp.StatusCode)
		if apiResp.Error != nil && apiResp.Error.Message != "" {
			msg = apiResp.Error.Message
		}
		return Response{}, &ProviderError{
			Provider: openAIVerifierProvider,
			Message:  msg,
		}
	}

	if len(apiResp.Choices) == 0 {
		latencyOutcome = "malformed"
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "no choices in response",
		}
	}

	rawContent := apiResp.Choices[0].Message.Content

	var result openAIVerifierResult
	if err := json.Unmarshal([]byte(rawContent), &result); err != nil {
		latencyOutcome = "malformed"
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "failed to parse structured response content",
			RawJSON:  rawContent,
		}
	}

	// Validate verdict is one of the three allowed values.
	switch result.Verdict {
	case "entailed", "contradicted", "insufficient":
		// valid — continue
	default:
		latencyOutcome = "malformed"
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  fmt.Sprintf("invalid verdict %q: must be entailed|contradicted|insufficient", result.Verdict),
			RawJSON:  rawContent,
		}
	}

	// Validate confidence is in [0, 1].
	if result.Confidence < 0 || result.Confidence > 1 {
		latencyOutcome = "malformed"
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  fmt.Sprintf("confidence %f out of range [0,1]", result.Confidence),
			RawJSON:  rawContent,
		}
	}

	// Validate rationale is non-empty.
	if strings.TrimSpace(result.Rationale) == "" {
		latencyOutcome = "malformed"
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "rationale must be non-empty",
			RawJSON:  rawContent,
		}
	}

	if apiResp.Usage != nil {
		observability.RecordVerifierTokens(ctx, v.metrics, v.model, apiResp.Usage.PromptTokens, apiResp.Usage.CompletionTokens, apiResp.Usage.TotalTokens)
	}
	latencyOutcome = "ok"
	return Response{
		Verdict:    result.Verdict,
		Confidence: result.Confidence,
		Reasoning:  result.Rationale,
		RawJSON:    rawContent,
	}, nil
}

func openAIVerifierTemperature(disabled bool) *float64 {
	if disabled {
		return nil
	}
	temperature := 0.0
	return &temperature
}

// prepareEvidence converts a single evidence string into the list format
// expected by the LLM payload. It strips control characters, enforces a
// per-item cap (openAIVerifierMaxItemChars) and a total-payload cap
// (openAIVerifierMaxTotalChars) via deterministic byte-level truncation.
func prepareEvidence(evidenceStr string) []string {
	if evidenceStr == "" {
		return []string{}
	}

	cleaned := stripControlChars(evidenceStr)
	if cleaned == "" {
		return []string{}
	}

	// Per-item cap: truncate to max item chars.
	if len(cleaned) > openAIVerifierMaxItemChars {
		cleaned = cleaned[:openAIVerifierMaxItemChars]
	}

	// Total cap: since we produce a single-item list here, the total equals
	// the item length. This guard ensures the invariant holds if future callers
	// produce multiple items.
	if len(cleaned) > openAIVerifierMaxTotalChars {
		cleaned = cleaned[:openAIVerifierMaxTotalChars]
	}

	return []string{cleaned}
}

// stripControlChars returns s with all Unicode control characters removed,
// preserving horizontal tab (\t), line feed (\n), and carriage return (\r)
// which are meaningful in natural-language evidence.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
