package skillpackservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	openAIConflictDeciderProvider       = "openai"
	openAIConflictDeciderDefaultTimeout = 60 * time.Second

	openAIConflictDeciderSystemPrompt = `You decide how to handle a dense-mem skill-pack import item against local knowledge.

Prefer preserving and extending the local knowledge base. Exact active duplicates should be skipped. Items that match superseded local facts are usually stale and should be skipped or demoted to a claim unless the source is explicitly trusted and clearly newer or more authoritative. Use supersede_local only when the imported item should replace active local facts. Use demote_to_claim when a source_fact is useful evidence but should not become an active fact.

Respond ONLY with a JSON object conforming to the required schema:
- "action": exactly one allowed action from the prompt
- "confidence": a float in [0.0, 1.0]
- "rationale": a concise, non-empty explanation`
)

var conflictDecisionResponseSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action":     {"type": "string", "enum": ["import_anyway", "skip", "supersede_local", "demote_to_claim"]},
		"confidence": {"type": "number"},
		"rationale":  {"type": "string"}
	},
	"required": ["action", "confidence", "rationale"],
	"additionalProperties": false
}`)

type openAIConflictDecisionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIConflictDecisionJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type openAIConflictDecisionResponseFormat struct {
	Type       string                           `json:"type"`
	JSONSchema openAIConflictDecisionJSONSchema `json:"json_schema"`
}

type openAIConflictDecisionRequest struct {
	Model          string                               `json:"model"`
	Messages       []openAIConflictDecisionMessage      `json:"messages"`
	Temperature    float64                              `json:"temperature"`
	ResponseFormat openAIConflictDecisionResponseFormat `json:"response_format"`
}

type openAIConflictDecisionAPIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

type openAIConflictDecisionResult struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

type OpenAIConflictDecider struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

var _ ConflictDecider = (*OpenAIConflictDecider)(nil)

func NewOpenAIConflictDecider(cfg config.ConfigProvider, httpClient *http.Client) *OpenAIConflictDecider {
	client := httpClient
	if client == nil {
		timeout := time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second
		if timeout == 0 {
			timeout = openAIConflictDeciderDefaultTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	return &OpenAIConflictDecider{
		baseURL:    cfg.GetAIVerifierAPIURL(),
		apiKey:     cfg.GetAIVerifierAPIKey(),
		model:      cfg.GetAIVerifierModel(),
		httpClient: client,
	}
}

func (d *OpenAIConflictDecider) Decide(ctx context.Context, req ConflictDecisionRequest) (ConflictDecisionResult, error) {
	userJSON, err := json.Marshal(req)
	if err != nil {
		return ConflictDecisionResult{}, &verifier.ProviderError{
			Provider: openAIConflictDeciderProvider,
			Message:  "failed to marshal conflict decision request",
			Cause:    err,
		}
	}

	chatReq := openAIConflictDecisionRequest{
		Model: d.model,
		Messages: []openAIConflictDecisionMessage{
			{Role: "system", Content: openAIConflictDeciderSystemPrompt},
			{Role: "user", Content: string(userJSON)},
		},
		Temperature: 0,
		ResponseFormat: openAIConflictDecisionResponseFormat{
			Type: "json_schema",
			JSONSchema: openAIConflictDecisionJSONSchema{
				Name:   "skill_pack_conflict_decision",
				Strict: true,
				Schema: conflictDecisionResponseSchema,
			},
		},
	}

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return ConflictDecisionResult{}, &verifier.ProviderError{
			Provider: openAIConflictDeciderProvider,
			Message:  "failed to marshal provider request",
			Cause:    err,
		}
	}

	url := strings.TrimSuffix(d.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ConflictDecisionResult{}, &verifier.ProviderError{
			Provider: openAIConflictDeciderProvider,
			Message:  "failed to create HTTP request",
			Cause:    err,
		}
	}
	httpReq.Header.Set("Authorization", "Bearer "+d.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := d.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ConflictDecisionResult{}, &verifier.TimeoutError{
				Provider: openAIConflictDeciderProvider,
				Message:  ctx.Err().Error(),
			}
		}
		return ConflictDecisionResult{}, &verifier.ProviderError{
			Provider: openAIConflictDeciderProvider,
			Message:  "HTTP request failed",
			Cause:    err,
		}
	}
	defer httpResp.Body.Close()

	var apiResp openAIConflictDecisionAPIResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&apiResp); err != nil {
		return ConflictDecisionResult{}, &verifier.MalformedResponseError{
			Provider: openAIConflictDeciderProvider,
			Message:  "failed to decode API response",
		}
	}

	if httpResp.StatusCode == http.StatusTooManyRequests {
		msg := "rate limited"
		if apiResp.Error != nil && apiResp.Error.Message != "" {
			msg = apiResp.Error.Message
		}
		return ConflictDecisionResult{}, &verifier.RateLimitError{
			Provider: openAIConflictDeciderProvider,
			Message:  msg,
		}
	}
	if httpResp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("unexpected status %d", httpResp.StatusCode)
		if apiResp.Error != nil && apiResp.Error.Message != "" {
			msg = apiResp.Error.Message
		}
		return ConflictDecisionResult{}, &verifier.ProviderError{
			Provider: openAIConflictDeciderProvider,
			Message:  msg,
		}
	}
	if len(apiResp.Choices) == 0 {
		return ConflictDecisionResult{}, &verifier.MalformedResponseError{
			Provider: openAIConflictDeciderProvider,
			Message:  "no choices in response",
		}
	}

	rawContent := apiResp.Choices[0].Message.Content
	var result openAIConflictDecisionResult
	if err := json.Unmarshal([]byte(rawContent), &result); err != nil {
		return ConflictDecisionResult{}, &verifier.MalformedResponseError{
			Provider: openAIConflictDeciderProvider,
			Message:  "failed to parse structured response content",
			RawJSON:  rawContent,
		}
	}
	if result.Action != DecisionImportAnyway &&
		result.Action != DecisionSkip &&
		result.Action != DecisionSupersedeLocal &&
		result.Action != DecisionDemoteToClaim {
		return ConflictDecisionResult{}, &verifier.MalformedResponseError{
			Provider: openAIConflictDeciderProvider,
			Message:  fmt.Sprintf("invalid action %q", result.Action),
			RawJSON:  rawContent,
		}
	}
	if result.Confidence < 0 || result.Confidence > 1 {
		return ConflictDecisionResult{}, &verifier.MalformedResponseError{
			Provider: openAIConflictDeciderProvider,
			Message:  fmt.Sprintf("confidence %f out of range [0,1]", result.Confidence),
			RawJSON:  rawContent,
		}
	}
	if strings.TrimSpace(result.Rationale) == "" {
		return ConflictDecisionResult{}, &verifier.MalformedResponseError{
			Provider: openAIConflictDeciderProvider,
			Message:  "rationale must be non-empty",
			RawJSON:  rawContent,
		}
	}

	return ConflictDecisionResult{
		Action:     result.Action,
		Confidence: result.Confidence,
		Rationale:  result.Rationale,
		Model:      d.model,
		RawJSON:    rawContent,
	}, nil
}
