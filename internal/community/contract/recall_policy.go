package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	DefaultRecallCommunityLimit             = 3
	MaxRecallCommunityLimit                 = 10
	DefaultRecallCommunityRelationshipLimit = 5
	MaxRecallCommunityRelationshipLimit     = 20
	MaxRecallCommunityTopEntities           = 5
)

type RecallCommunityMatchCategory uint8

const (
	RecallCommunityReturnedEvidenceOverlap RecallCommunityMatchCategory = iota
	RecallCommunityKnownContextOverlap
	RecallCommunitySeedOverlap
	RecallCommunityRemainingMatch
)

func RecallCommunityMatchOrder() [4]RecallCommunityMatchCategory {
	return [4]RecallCommunityMatchCategory{
		RecallCommunityReturnedEvidenceOverlap,
		RecallCommunityKnownContextOverlap,
		RecallCommunitySeedOverlap,
		RecallCommunityRemainingMatch,
	}
}

func NormalizeCommunityRecallInput(input CommunityRecallInput) CommunityRecallInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 {
		input.Limit = DefaultRecallCommunityLimit
	}
	if input.Limit > MaxRecallCommunityLimit {
		input.Limit = MaxRecallCommunityLimit
	}
	if input.RelationshipLimit <= 0 {
		input.RelationshipLimit = DefaultRecallCommunityRelationshipLimit
	}
	if input.RelationshipLimit > MaxRecallCommunityRelationshipLimit {
		input.RelationshipLimit = MaxRecallCommunityRelationshipLimit
	}
	input.KnownEvidenceIDs = NormalizeCommunityIDs(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = NormalizeCommunityIDs(input.KnownRelationshipIDs)
	input.ReturnedEvidenceIDs = NormalizeCommunityIDs(input.ReturnedEvidenceIDs)
	input.SeedRelationshipIDs = NormalizeCommunityIDs(input.SeedRelationshipIDs)
	input.ExpandFromEntityIDs = NormalizeCommunityIDs(input.ExpandFromEntityIDs)
	input.ExcludedGroupKeys = normalizeCommunityStrings(input.ExcludedGroupKeys)
	input.CoveredGroupKeys = normalizeCommunityStrings(input.CoveredGroupKeys)
	input.CoveredGroupKeys = appendUniqueCommunityStrings(input.CoveredGroupKeys, input.ExcludedGroupKeys...)
	return input
}

func appendUniqueCommunityStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func ValidateCommunityRecallInput(input CommunityRecallInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	return nil
}

func NormalizeCommunityIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		if _, err := uuid.Parse(value); err != nil {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
func normalizeCommunityStrings(values []string) []string {
	out := make([]string, 0, len(values))
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
	}
	return out
}
