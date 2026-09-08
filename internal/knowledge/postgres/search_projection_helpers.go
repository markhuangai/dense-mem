package postgres

import (
	"context"

	"gorm.io/gorm"
)

func refreshRelationshipProjectionGeneration(ctx context.Context, tx *gorm.DB, teamID, projectionGenerationID string) error {
	return tx.WithContext(ctx).Exec("UPDATE search_projection_generations SET drifted_count = 0 WHERE team_id = ?::uuid AND projection_generation_id = ?::uuid", teamID, projectionGenerationID).Error
}
