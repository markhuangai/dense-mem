package contract

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const (
	DefaultRecallHypothesisLimit  = 5
	MaxRecallHypothesisLimit      = 20
	MaxRecallHypothesisContextIDs = 200
)

type RecallHypothesisMatchCategory uint8

const (
	RecallHypothesisSourceOverlap RecallHypothesisMatchCategory = iota
	RecallHypothesisEndpointOverlap
	RecallHypothesisStatementMatch
	RecallHypothesisRationaleMatch
	RecallHypothesisFallback
)

func RecallHypothesisMatchOrder() [5]RecallHypothesisMatchCategory {
	return [5]RecallHypothesisMatchCategory{
		RecallHypothesisSourceOverlap,
		RecallHypothesisEndpointOverlap,
		RecallHypothesisStatementMatch,
		RecallHypothesisRationaleMatch,
		RecallHypothesisFallback,
	}
}

func NormalizeRecallHypothesesInput(input RecallHypothesesInput) (RecallHypothesesInput, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 {
		input.Limit = DefaultRecallHypothesisLimit
	}
	if input.Limit > MaxRecallHypothesisLimit {
		input.Limit = MaxRecallHypothesisLimit
	}
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return input, fmt.Errorf("team_id is required: %w", err)
	}
	var err error
	if input.EvidenceIDs, err = normalizeRecallHypothesisContextIDs(input.EvidenceIDs); err != nil {
		return input, fmt.Errorf("recall context evidence IDs: %w", err)
	}
	if input.RelationshipIDs, err = normalizeRecallHypothesisContextIDs(input.RelationshipIDs); err != nil {
		return input, fmt.Errorf("recall context Relationship IDs: %w", err)
	}
	if input.EntityIDs, err = normalizeRecallHypothesisContextIDs(input.EntityIDs); err != nil {
		return input, fmt.Errorf("recall context entity IDs: %w", err)
	}
	if input.ValueIDs, err = normalizeRecallHypothesisContextIDs(input.ValueIDs); err != nil {
		return input, fmt.Errorf("recall context Value IDs: %w", err)
	}
	return input, nil
}

func normalizeRecallHypothesisContextIDs(values []string) ([]string, error) {
	out := make([]string, 0, min(len(values), MaxRecallHypothesisContextIDs))
	seen := make(map[string]struct{}, cap(out))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, err
		}
		value = parsed.String()
		if _, exists := seen[value]; exists {
			continue
		}
		if len(out) == MaxRecallHypothesisContextIDs {
			break
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}
