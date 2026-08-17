package repository

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func normalizeRecallRelationshipsInput(input RecallRelationshipsInput) RecallRelationshipsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownEvidenceIDs = normalizeRecallUUIDList(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = normalizeRecallUUIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeRecallUUIDList(input.ExpandFromEntityIDs)
	input.ExcludedGroupKeys = normalizeRecallStringList(input.ExcludedGroupKeys)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.SpaceKind = strings.TrimSpace(input.SpaceKind)
	if input.Limit <= 0 {
		input.Limit = defaultRelationshipRecallLimit
	}
	if input.Limit > maxRelationshipRecallLimit {
		input.Limit = maxRelationshipRecallLimit
	}
	return input
}

func validateRecallRelationshipsInput(input RecallRelationshipsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.SpaceKind != "" && !domain.MemorySpaceKind(input.SpaceKind).Valid() {
		return fmt.Errorf("space_kind is invalid: %s", input.SpaceKind)
	}
	if input.SpaceKind != "" && input.SpaceKind != string(domain.MemorySpaceTeamShared) && input.SpaceID == "" {
		return fmt.Errorf("space_id is required for private space kind %s", input.SpaceKind)
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
	}
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return errors.New("query or expand_from_entity_ids is required")
	}
	for label, values := range map[string][]string{
		"known_evidence_ids":     input.KnownEvidenceIDs,
		"known_relationship_ids": input.KnownRelationshipIDs,
		"expand_from_entity_ids": input.ExpandFromEntityIDs,
	} {
		for _, value := range values {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("%s contains invalid UUID %q: %w", label, value, err)
			}
		}
	}
	return nil
}

func normalizeRecallStringList(values []string) []string {
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
	sort.Strings(out)
	return out
}
