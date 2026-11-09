package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	openAIVerifierProvider      = "openai"
	openAIVerifierMaxItemChars  = 4000
	openAIVerifierMaxTotalChars = 32000

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

type openAIVerifierMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIVerifierResult struct {
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

type OpenAIVerifier struct {
	transport        modelprovider.StructuredTransport
	model            string
	assessmentLimits SemanticAssessmentLimits
	logger           observability.LogProvider
}

var _ Verifier = (*OpenAIVerifier)(nil)

func NewOpenAIVerifier(transport modelprovider.StructuredTransport, model string, limits SemanticAssessmentLimits, logger observability.LogProvider) *OpenAIVerifier {
	normalized := normalizeSemanticAssessmentLimits(limits)
	normalized.LegacyProviderFraming = true
	normalized.ProviderModel = strings.TrimSpace(model)
	return &OpenAIVerifier{transport: transport, model: normalized.ProviderModel, assessmentLimits: normalized, logger: logger}
}

func (v *OpenAIVerifier) SetMetrics(metrics observability.DiscoverabilityMetrics) {
	if provider, ok := v.transport.(interface {
		SetMetrics(observability.DiscoverabilityMetrics)
	}); ok {
		provider.SetMetrics(metrics)
	}
}

func (v *OpenAIVerifier) ModelName() string { return v.model }

func (v *OpenAIVerifier) StructuredChatJSON(ctx context.Context, model, schemaName string, schema map[string]any, prompt string, payload any) (string, error) {
	return v.openAIStructuredChatJSON(ctx, model, schemaName, schema, prompt, payload)
}

func (v *OpenAIVerifier) Verify(ctx context.Context, req Request) (Response, error) {
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

	var schema map[string]any
	if err := json.Unmarshal(verifierResponseSchema, &schema); err != nil {
		return Response{}, err
	}
	reply, err := v.openAIStructuredChatMessagesJSONWithUsage(ctx, v.model, "verification_result", schema, []openAIVerifierMessage{{Role: "system", Content: openAIVerifierSystemPrompt}, {Role: "user", Content: string(userJSON)}})
	if err != nil {
		return Response{}, err
	}
	rawContent := reply.Content
	var result openAIVerifierResult
	if err := json.Unmarshal([]byte(rawContent), &result); err != nil {
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
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  fmt.Sprintf("invalid verdict %q: must be entailed|contradicted|insufficient", result.Verdict),
			RawJSON:  rawContent,
		}
	}

	// Validate confidence is in [0, 1].
	if result.Confidence < 0 || result.Confidence > 1 {
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  fmt.Sprintf("confidence %f out of range [0,1]", result.Confidence),
			RawJSON:  rawContent,
		}
	}

	// Validate rationale is non-empty.
	if strings.TrimSpace(result.Rationale) == "" {
		return Response{}, &MalformedResponseError{
			Provider: openAIVerifierProvider,
			Message:  "rationale must be non-empty",
			RawJSON:  rawContent,
		}
	}

	return Response{
		Verdict:    result.Verdict,
		Confidence: result.Confidence,
		Reasoning:  result.Rationale,
		RawJSON:    rawContent,
	}, nil
}

func (v *OpenAIVerifier) openAIStructuredChatJSON(ctx context.Context, model, schemaName string, schema map[string]any, prompt string, payload any) (string, error) {
	result, err := v.openAIStructuredChatJSONWithUsage(ctx, model, schemaName, schema, prompt, payload)
	return result.Content, err
}

func (v *OpenAIVerifier) openAIStructuredChatJSONWithUsage(ctx context.Context, model, schemaName string, schema map[string]any, prompt string, payload any) (openAIStructuredChatResult, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return openAIStructuredChatResult{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal user payload", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
	}
	return v.openAIStructuredChatMessagesJSONWithUsage(ctx, model, schemaName, schema, []openAIVerifierMessage{{Role: "system", Content: prompt}, {Role: "user", Content: string(encoded)}})
}

func (v *OpenAIVerifier) openAIStructuredChatMessagesJSONWithUsage(ctx context.Context, model, schemaName string, schema map[string]any, messages []openAIVerifierMessage) (openAIStructuredChatResult, error) {
	if v.transport == nil {
		return openAIStructuredChatResult{}, &ProviderError{Provider: openAIVerifierProvider, Message: "structured transport is required", FailureClass: ProviderFailureClassProviderUnavailable}
	}
	converted := make([]modelprovider.Message, len(messages))
	for index, message := range messages {
		converted[index] = modelprovider.Message{Role: message.Role, Content: message.Content}
	}
	result, err := v.transport.Complete(ctx, modelprovider.StructuredRequest{Model: model, SchemaName: schemaName, Schema: schema, Messages: converted, MaxInputTokens: v.assessmentLimits.MaxInputTokens, MaxOutputTokens: v.assessmentLimits.MaxOutputTokens})
	reply := openAIStructuredChatResult{Content: result.Content, Diagnostics: result.Diagnostics}
	if result.UsageAvailable {
		reply.ReportedUsage = &openAIVerifierUsage{PromptTokens: int64(result.PromptTokens), CompletionTokens: int64(result.CompletionTokens), TotalTokens: int64(result.TotalTokens)}
		if result.PromptTokens > 0 && result.CompletionTokens > 0 {
			reply.Usage = reply.ReportedUsage
		}
	}
	return reply, err
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
