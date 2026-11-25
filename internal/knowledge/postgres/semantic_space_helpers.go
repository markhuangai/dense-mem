package postgres

import (
	"context"
	"errors"
	"fmt"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type semanticSpaceFence struct {
	ID         string
	Generation int64
}

// activeSemanticSpaceGenerationSQL keeps reads on the active generation after a private space is sealed.
func activeSemanticSpaceGenerationSQL(alias string) string {
	return storagepostgres.ActiveSemanticSpaceGenerationSQL(alias)
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

func requireSemanticSpaceMatch(expected, actual string) error {
	if expected == "" || actual == "" {
		return errors.New("semantic lineage memory space is required")
	}
	if expected == actual {
		return nil
	}
	return errors.New("semantic lineage crosses memory spaces")
}
