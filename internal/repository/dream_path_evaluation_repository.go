package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) ListUnassessedDreamPaths(ctx context.Context, teamID string, paths []DreamPathEvaluationInput) ([]DreamPathEvaluationInput, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	paths = normalizeDreamPathEvaluationInputs(paths)
	if err := validateDreamPathEvaluationInputs(paths); err != nil {
		return nil, err
	}
	unassessed := make([]DreamPathEvaluationInput, 0, len(paths))
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		for _, path := range paths {
			var exists bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM dream_path_evaluations
					WHERE team_id = ?::uuid
					  AND first_relationship_id = ?::uuid
					  AND first_relationship_version = ?
					  AND second_relationship_id = ?::uuid
					  AND second_relationship_version = ?
				)
			`, teamID, path.FirstRelationshipID, path.FirstRelationshipVersion,
				path.SecondRelationshipID, path.SecondRelationshipVersion).Scan(&exists).Error; err != nil {
				return err
			}
			if !exists {
				unassessed = append(unassessed, path)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: list unassessed paths: %w", err)
	}
	return unassessed, nil
}

func (r *SemanticRepositoryImpl) RecordDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput) error {
	return r.recordDreamPathEvaluations(ctx, input, false)
}

func (r *SemanticRepositoryImpl) RecordScheduledDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput) error {
	return r.recordDreamPathEvaluations(ctx, input, true)
}

func (r *SemanticRepositoryImpl) recordDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput, system bool) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.CreatedByProfileID = strings.TrimSpace(input.CreatedByProfileID)
	input.ProviderModel = strings.TrimSpace(input.ProviderModel)
	input.Paths = normalizeDreamPathEvaluationInputs(input.Paths)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if !system {
		if _, err := uuid.Parse(input.CreatedByProfileID); err != nil {
			return fmt.Errorf("created_by_profile_id is required: %w", err)
		}
	}
	if input.ProviderModel == "" {
		return fmt.Errorf("provider_model is required")
	}
	if err := validateDreamPathEvaluationInputs(input.Paths); err != nil {
		return err
	}
	if len(input.Paths) == 0 {
		return nil
	}
	err := r.withDreamWriteTx(ctx, input.TeamID, input.CreatedByProfileID, system, func(tx *gorm.DB) error {
		return insertDreamPathEvaluationsTx(ctx, tx, input.TeamID, input.ProviderModel, input.Paths)
	})
	if err != nil {
		return fmt.Errorf("dream: record path evaluations: %w", err)
	}
	return nil
}

func insertDreamPathEvaluationsTx(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	providerModel string,
	paths []DreamPathEvaluationInput,
) error {
	for _, path := range paths {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO dream_path_evaluations (
			    team_id, first_relationship_id, first_relationship_version,
			    second_relationship_id, second_relationship_version, provider_model
		) VALUES (?::uuid, ?::uuid, ?, ?::uuid, ?, ?)
		ON CONFLICT (team_id, first_relationship_id, first_relationship_version,
		             second_relationship_id, second_relationship_version)
		DO NOTHING
	`, teamID, path.FirstRelationshipID, path.FirstRelationshipVersion,
			path.SecondRelationshipID, path.SecondRelationshipVersion, providerModel).Error; err != nil {
			return err
		}
	}
	return nil
}

func normalizeDreamPathEvaluationInputs(paths []DreamPathEvaluationInput) []DreamPathEvaluationInput {
	seen := map[string]struct{}{}
	result := make([]DreamPathEvaluationInput, 0, len(paths))
	for _, path := range paths {
		path.FirstRelationshipID = strings.TrimSpace(path.FirstRelationshipID)
		path.SecondRelationshipID = strings.TrimSpace(path.SecondRelationshipID)
		key := fmt.Sprintf("%s:%d:%s:%d", path.FirstRelationshipID, path.FirstRelationshipVersion, path.SecondRelationshipID, path.SecondRelationshipVersion)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result
}

func validateDreamPathEvaluationInputs(paths []DreamPathEvaluationInput) error {
	for index, path := range paths {
		for field, value := range map[string]string{
			"first_relationship_id":  path.FirstRelationshipID,
			"second_relationship_id": path.SecondRelationshipID,
		} {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("paths[%d].%s is invalid: %w", index, field, err)
			}
		}
		if path.FirstRelationshipID == path.SecondRelationshipID || path.FirstRelationshipVersion < 1 || path.SecondRelationshipVersion < 1 {
			return fmt.Errorf("paths[%d] is not a valid directed relationship pair", index)
		}
	}
	return nil
}
