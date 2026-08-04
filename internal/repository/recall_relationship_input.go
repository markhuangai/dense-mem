package repository

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func normalizeRecallRelationshipsInput(input RecallRelationshipsInput) RecallRelationshipsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownRelationshipIDs = normalizeRecallUUIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeRecallUUIDList(input.ExpandFromEntityIDs)
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
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return errors.New("query or expand_from_entity_ids is required")
	}
	for label, values := range map[string][]string{
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
