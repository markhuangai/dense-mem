package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const communitySummaryPrompt = `You are Dense-Mem's community summarizer. Use only the supplied current semantic relationships and bounded evidence identifiers. Return a complete JSON object. The summary must describe a useful common topic without asserting unsupported facts. Select top_entities exactly from the supplied relationship Subject or Object values, and select top_predicates exactly from the supplied relationship Predicate values; use an empty array when there is no suitable value. admitted_relationship_ids and admitted_evidence_ids may contain only identifiers present in the input. Never invent IDs, truth, ownership, lifecycle, or policy decisions.`

var communitySummarySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
		"top_entities":              map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "maxItems": 5},
		"top_predicates":            map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "maxItems": 5},
		"admitted_relationship_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "uuid"}, "maxItems": 1000},
		"admitted_evidence_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "uuid"}, "maxItems": 2000},
		"admitted_support_quotes": map[string]any{"type": "array", "maxItems": 2000, "items": map[string]any{"type": "object", "properties": map[string]any{
			"evidence_id": map[string]any{"type": "string", "format": "uuid"},
			"quote":       map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
		}, "required": []string{"evidence_id", "quote"}, "additionalProperties": false}},
	},
	"required":             []string{"summary", "top_entities", "top_predicates", "admitted_relationship_ids", "admitted_evidence_ids", "admitted_support_quotes"},
	"additionalProperties": false,
}

type communitySummaryResponse struct {
	Summary                 string                                `json:"summary"`
	TopEntities             []string                              `json:"top_entities"`
	TopPredicates           []string                              `json:"top_predicates"`
	AdmittedRelationshipIDs []string                              `json:"admitted_relationship_ids"`
	AdmittedEvidenceIDs     []string                              `json:"admitted_evidence_ids"`
	AdmittedSupportQuotes   []domain.CommunitySummarySupportQuote `json:"admitted_support_quotes"`
}

// SummarizeCommunity performs one complete bounded provider attempt. The
// caller owns the retry and all-or-nothing validation policy.
func (v *OpenAIVerifier) SummarizeCommunity(ctx context.Context, input domain.CommunitySummaryInput) (domain.CommunitySummary, error) {
	if v == nil {
		return domain.CommunitySummary{}, &ProviderError{Provider: openAIVerifierProvider, Message: "community summary provider is unavailable"}
	}
	if strings.TrimSpace(input.CommunityID) == "" || len(input.Relationships) == 0 {
		return domain.CommunitySummary{}, &ProviderError{Provider: openAIVerifierProvider, Message: "community summary input is empty"}
	}
	ctx = observability.WithAIOperation(ctx, observability.AIOperationCommunitySummary, len(input.Relationships))
	content, err := v.openAIStructuredChatJSON(ctx, v.model, "community_summary", communitySummarySchema, communitySummaryPrompt, input)
	if err != nil {
		return domain.CommunitySummary{}, err
	}
	var response communitySummaryResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return domain.CommunitySummary{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "failed to parse community summary response", RawJSON: content}
	}
	if strings.TrimSpace(response.Summary) == "" {
		return domain.CommunitySummary{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "community summary is empty", RawJSON: content}
	}
	return domain.CommunitySummary{
		Summary: strings.TrimSpace(response.Summary), TopEntities: boundedStrings(response.TopEntities, 5), TopPredicates: boundedStrings(response.TopPredicates, 5),
		AdmittedRelationshipIDs: append([]string(nil), response.AdmittedRelationshipIDs...), AdmittedEvidenceIDs: append([]string(nil), response.AdmittedEvidenceIDs...),
		AdmittedSupportQuotes: append([]domain.CommunitySummarySupportQuote(nil), response.AdmittedSupportQuotes...),
		InputHash:             input.SummaryInputHash, ProviderModel: v.model,
		ResponseHash: hashCommunityResponse(content),
	}, nil
}

func boundedStrings(values []string, limit int) []string {
	out := make([]string, 0, minInt(len(values), limit))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	return out
}

func hashCommunityResponse(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ interface {
	ModelName() string
	SummarizeCommunity(context.Context, domain.CommunitySummaryInput) (domain.CommunitySummary, error)
} = (*OpenAIVerifier)(nil)
