package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type semanticSpaceFence struct {
	ID         string
	Generation int64
}

// activeSemanticSpaceGenerationSQL keeps reads on the active generation after a private space is sealed.
func activeSemanticSpaceGenerationSQL(alias string) string {
	return fmt.Sprintf(`%s.space_generation = dense_mem_active_space_generation(%s.team_id, %s.space_id)`, alias, alias, alias)
}

func loadTeamSharedSpaceFence(ctx context.Context, tx *gorm.DB, teamID string) (semanticSpaceFence, error) {
	fence := semanticSpaceFence{}
	err := tx.WithContext(ctx).Raw(`
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = ?::uuid
		  AND kind = 'team_shared'
		  AND lifecycle_state = 'active'
	`, teamID).Row().Scan(&fence.ID, &fence.Generation)
	if err != nil {
		return semanticSpaceFence{}, fmt.Errorf("load team-shared memory space: %w", err)
	}
	return fence, nil
}

func loadSemanticInputSpaceID(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput) (string, error) {
	var ingestSpaceID string
	var placementID, placementSpaceID sql.NullString
	err := tx.WithContext(ctx).Raw(`
		SELECT ingest.space_id::text,
		       placement.placement_item_id::text,
		       placement.space_id::text
		FROM knowledge_ingests AS ingest
		LEFT JOIN placement_items AS placement
		  ON placement.team_id = ingest.team_id
		 AND placement.ingest_id = ingest.ingest_id
		 AND placement.owner_profile_id = ingest.owner_profile_id
		 AND placement.placement_item_id = NULLIF(?, '')::uuid
		WHERE ingest.team_id = ?::uuid
		  AND ingest.ingest_id = ?::uuid
		  AND ingest.owner_profile_id = ?::uuid
	`, input.PlacementItemID, input.TeamID, input.IngestID, input.OwnerProfileID).Row().Scan(
		&ingestSpaceID, &placementID, &placementSpaceID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: semantic ingest does not exist", ErrSemanticOwnerMismatch)
	}
	if err != nil {
		return "", err
	}
	if input.PlacementItemID != "" && !placementID.Valid {
		return "", errors.New("semantic placement item does not belong to ingest")
	}
	if placementSpaceID.Valid && placementSpaceID.String != ingestSpaceID {
		return "", errors.New("semantic placement and ingest memory spaces differ")
	}
	if ingestSpaceID == "" {
		return "", errors.New("semantic ingest has no memory space")
	}
	return ingestSpaceID, nil
}

func requireSemanticSpaceMatch(expected, actual string) error {
	if expected == "" || actual == "" {
		return errors.New("semantic lineage memory space is required")
	}
	if expected == actual {
		return nil
	}
	return errors.New("semantic lineage crosses memory spaces")
}
