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
	err := tx.WithContext(ctx).Raw(`
		SELECT ingest.space_id::text
		FROM knowledge_ingests AS ingest
		WHERE ingest.team_id = ?::uuid
		  AND ingest.ingest_id = ?::uuid
		  AND ingest.owner_profile_id = ?::uuid
	`, input.TeamID, input.IngestID, input.OwnerProfileID).Row().Scan(&ingestSpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: semantic ingest does not exist", ErrSemanticOwnerMismatch)
	}
	if err != nil {
		return "", err
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
