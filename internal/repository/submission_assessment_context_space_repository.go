package repository

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"
)

func validateSubmissionAssessmentContextSpaces(
	ctx context.Context,
	tx *gorm.DB,
	input CommitSubmissionAssessmentInput,
) error {
	var spaceID sql.NullString
	var spaceGeneration sql.NullInt64
	err := tx.WithContext(ctx).Raw(`
		SELECT space_id::text, space_generation
		FROM placement_runs
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND placement_run_id = ?::uuid
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID).Row().Scan(&spaceID, &spaceGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSubmissionAssessmentScopeMismatch
	}
	if err != nil {
		return err
	}
	spaceValue := ""
	if spaceID.Valid {
		spaceValue = spaceID.String
	}
	generationValue := int64(0)
	if spaceGeneration.Valid {
		generationValue = spaceGeneration.Int64
	}
	for _, entry := range input.RelationshipObservations {
		if target := entry.Observation.CorrectionTarget; target != nil {
			var found bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM relationship_records
					WHERE team_id = ?::uuid
					  AND relationship_id = ?::uuid
					  AND owner_profile_id = ?::uuid
					  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
					  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
				)
			`, input.TeamID, target.RelationshipID, input.OwnerProfileID, spaceValue, generationValue).Scan(&found).Error; err != nil {
				return err
			}
			if !found {
				return ErrCorrectionTargetStale
			}
		}
		if conflict := entry.Observation.ConflictContext; conflict != nil {
			var found bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM relationship_conflict_cases
					WHERE team_id = ?::uuid
					  AND conflict_id = ?::uuid
					  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
					  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
				)
			`, input.TeamID, conflict.ConflictID, spaceValue, generationValue).Scan(&found).Error; err != nil {
				return err
			}
			if !found {
				return ErrConflictContextStale
			}
		}
	}
	return nil
}
