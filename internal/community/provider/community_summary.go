// Package provider owns Community's structured summary schema and response
// decoding. Transport and provider-specific error translation remain adapters.
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const Prompt = `You are Dense-Mem's community summarizer. Use only the supplied current semantic relationships and bounded evidence identifiers. Return a complete JSON object. The summary must describe a useful common topic without asserting unsupported facts. Select top_entities exactly from the supplied relationship Subject or Object values, and select top_predicates exactly from the supplied relationship Predicate values; use an empty array when there is no suitable value. admitted_relationship_ids and admitted_evidence_ids may contain only identifiers present in the input. Never invent IDs, truth, ownership, lifecycle, or policy decisions.`

var ResponseSchema = map[string]any{
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

type CompleteFunc func(context.Context, string, string, map[string]any, string, any) (string, error)

type MalformedResponseError struct {
	Message string
	RawJSON string
}

func (e *MalformedResponseError) Error() string { return e.Message }

type summaryResponse struct {
	Summary                 string                                `json:"summary"`
	TopEntities             []string                              `json:"top_entities"`
	TopPredicates           []string                              `json:"top_predicates"`
	AdmittedRelationshipIDs []string                              `json:"admitted_relationship_ids"`
	AdmittedEvidenceIDs     []string                              `json:"admitted_evidence_ids"`
	AdmittedSupportQuotes   []domain.CommunitySummarySupportQuote `json:"admitted_support_quotes"`
}

func SummarizeCommunity(ctx context.Context, model string, input domain.CommunitySummaryInput, complete CompleteFunc) (domain.CommunitySummary, error) {
	if complete == nil || strings.TrimSpace(input.CommunityID) == "" || len(input.Relationships) == 0 {
		return domain.CommunitySummary{}, errors.New("community summary provider input is unavailable")
	}
	content, err := complete(ctx, model, "community_summary", ResponseSchema, Prompt, input)
	if err != nil {
		return domain.CommunitySummary{}, err
	}
	var response summaryResponse
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return domain.CommunitySummary{}, &MalformedResponseError{Message: "failed to parse community summary response", RawJSON: content}
	}
	if strings.TrimSpace(response.Summary) == "" {
		return domain.CommunitySummary{}, &MalformedResponseError{Message: "community summary is empty", RawJSON: content}
	}
	return domain.CommunitySummary{
		Summary:                 strings.TrimSpace(response.Summary),
		TopEntities:             append([]string(nil), response.TopEntities...),
		TopPredicates:           append([]string(nil), response.TopPredicates...),
		AdmittedRelationshipIDs: append([]string(nil), response.AdmittedRelationshipIDs...),
		AdmittedEvidenceIDs:     append([]string(nil), response.AdmittedEvidenceIDs...),
		AdmittedSupportQuotes:   append([]domain.CommunitySummarySupportQuote(nil), response.AdmittedSupportQuotes...),
		InputHash:               input.SummaryInputHash,
		ProviderModel:           model,
		ResponseHash:            hashResponse(content),
	}, nil
}

func hashResponse(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
