package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
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
	if len(paths) == 0 {
		return []DreamPathEvaluationInput{}, nil
	}
	payload, err := marshalDreamPathEvaluationInputs(paths)
	if err != nil {
		return nil, err
	}
	unassessed := make([]DreamPathEvaluationInput, 0, len(paths))
	err = r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH candidate_paths AS (
				SELECT first_relationship_id::uuid AS first_relationship_id,
				       first_relationship_version,
				       second_relationship_id::uuid AS second_relationship_id,
				       second_relationship_version,
				       allowed_predicate_fingerprint,
				       ordinal
				FROM jsonb_to_recordset(?::jsonb) AS candidate(
					first_relationship_id text,
					first_relationship_version integer,
					second_relationship_id text,
					second_relationship_version integer,
					allowed_predicate_fingerprint text,
					ordinal integer
				)
			)
			SELECT candidate.first_relationship_id::text,
			       candidate.first_relationship_version,
			       candidate.second_relationship_id::text,
			       candidate.second_relationship_version,
			       candidate.allowed_predicate_fingerprint
			FROM candidate_paths candidate
			LEFT JOIN dream_path_evaluations evaluation
			  ON evaluation.team_id = ?::uuid
			 AND evaluation.first_relationship_id = candidate.first_relationship_id
			 AND evaluation.first_relationship_version = candidate.first_relationship_version
			 AND evaluation.second_relationship_id = candidate.second_relationship_id
			 AND evaluation.second_relationship_version = candidate.second_relationship_version
			 AND evaluation.allowed_predicate_fingerprint = candidate.allowed_predicate_fingerprint
			WHERE evaluation.path_evaluation_id IS NULL
			ORDER BY candidate.ordinal
		`, string(payload), teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var path DreamPathEvaluationInput
			if err := rows.Scan(
				&path.FirstRelationshipID,
				&path.FirstRelationshipVersion,
				&path.SecondRelationshipID,
				&path.SecondRelationshipVersion,
				&path.AllowedPredicateFingerprint,
			); err != nil {
				return err
			}
			unassessed = append(unassessed, path)
		}
		return rows.Err()
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
	payload, err := marshalDreamPathEvaluationInputs(paths)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		WITH candidate_paths AS (
			SELECT first_relationship_id::uuid AS first_relationship_id,
			       first_relationship_version,
			       second_relationship_id::uuid AS second_relationship_id,
			       second_relationship_version,
			       allowed_predicate_fingerprint
			FROM jsonb_to_recordset(?::jsonb) AS candidate(
				first_relationship_id text,
				first_relationship_version integer,
				second_relationship_id text,
				second_relationship_version integer,
				allowed_predicate_fingerprint text,
				ordinal integer
			)
		)
		INSERT INTO dream_path_evaluations (
		    team_id, first_relationship_id, first_relationship_version,
		    second_relationship_id, second_relationship_version,
		    allowed_predicate_fingerprint, provider_model
		)
		SELECT ?::uuid,
		       first_relationship_id,
		       first_relationship_version,
		       second_relationship_id,
		       second_relationship_version,
		       allowed_predicate_fingerprint,
		       ?
		FROM candidate_paths
		ON CONFLICT (team_id, first_relationship_id, first_relationship_version,
		             second_relationship_id, second_relationship_version,
		             allowed_predicate_fingerprint)
		DO NOTHING
	`, string(payload), teamID, providerModel).Error
}

func normalizeDreamPathEvaluationInputs(paths []DreamPathEvaluationInput) []DreamPathEvaluationInput {
	seen := map[string]struct{}{}
	result := make([]DreamPathEvaluationInput, 0, len(paths))
	for _, path := range paths {
		path.FirstRelationshipID = strings.TrimSpace(path.FirstRelationshipID)
		path.SecondRelationshipID = strings.TrimSpace(path.SecondRelationshipID)
		path.AllowedPredicateFingerprint = strings.ToLower(strings.TrimSpace(path.AllowedPredicateFingerprint))
		key := fmt.Sprintf("%s:%d:%s:%d:%s", path.FirstRelationshipID, path.FirstRelationshipVersion, path.SecondRelationshipID, path.SecondRelationshipVersion, path.AllowedPredicateFingerprint)
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
		if len(path.AllowedPredicateFingerprint) != 64 {
			return fmt.Errorf("paths[%d].allowed_predicate_fingerprint must be a SHA-256 hex digest", index)
		}
		if _, err := hex.DecodeString(path.AllowedPredicateFingerprint); err != nil {
			return fmt.Errorf("paths[%d].allowed_predicate_fingerprint must be a SHA-256 hex digest: %w", index, err)
		}
	}
	return nil
}

type dreamPathEvaluationPayload struct {
	FirstRelationshipID         string `json:"first_relationship_id"`
	FirstRelationshipVersion    int    `json:"first_relationship_version"`
	SecondRelationshipID        string `json:"second_relationship_id"`
	SecondRelationshipVersion   int    `json:"second_relationship_version"`
	AllowedPredicateFingerprint string `json:"allowed_predicate_fingerprint"`
	Ordinal                     int    `json:"ordinal"`
}

func marshalDreamPathEvaluationInputs(paths []DreamPathEvaluationInput) ([]byte, error) {
	payload := make([]dreamPathEvaluationPayload, 0, len(paths))
	for index, path := range paths {
		payload = append(payload, dreamPathEvaluationPayload{
			FirstRelationshipID:         path.FirstRelationshipID,
			FirstRelationshipVersion:    path.FirstRelationshipVersion,
			SecondRelationshipID:        path.SecondRelationshipID,
			SecondRelationshipVersion:   path.SecondRelationshipVersion,
			AllowedPredicateFingerprint: path.AllowedPredicateFingerprint,
			Ordinal:                     index,
		})
	}
	return json.Marshal(payload)
}
