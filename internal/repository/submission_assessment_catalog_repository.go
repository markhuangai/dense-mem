package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	submissionAssessmentMaxEntityTargets = 400
	submissionAssessmentMaxCandidateSet  = 20
)

func (r *SemanticRepositoryImpl) ListSubmissionAssessmentEntityCatalog(
	ctx context.Context,
	input SubmissionAssessmentEntityCatalogInput,
) (SubmissionAssessmentEntityCatalogResult, error) {
	input = normalizeSubmissionAssessmentEntityCatalogInput(input)
	if err := validateSubmissionAssessmentEntityCatalogInput(input); err != nil {
		return SubmissionAssessmentEntityCatalogResult{}, err
	}
	result := SubmissionAssessmentEntityCatalogResult{
		Groups:   make([]SubmissionAssessmentEntityCatalogGroup, 0, len(input.Entities)),
		Complete: true,
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		for _, target := range input.Entities {
			group := SubmissionAssessmentEntityCatalogGroup{Ref: target.Ref, Candidates: []SemanticReviewEntityCandidate{}, Complete: true}
			rows, err := tx.WithContext(ctx).Raw(`
				WITH candidates AS (
				    SELECT DISTINCT ON (rec.entity_id)
				           rec.team_id::text, rec.entity_id::text, rec.entity_kind,
			           COALESCE(canonical.display_name, name.display_name, '') AS canonical_name,
				           ARRAY(
				               SELECT active_name.display_name
				               FROM entity_names AS active_name
				               WHERE active_name.team_id = rec.team_id
				                 AND active_name.entity_id = rec.entity_id
				                 AND active_name.valid_to IS NULL
				                 AND active_name.name_kind IN ('canonical', 'alias')
				               ORDER BY active_name.name_kind, active_name.created_at, active_name.entity_name_id
				           ) AS active_names,
				           rec.identity_context, rec.status,
				           CASE WHEN rec.entity_id = NULLIF(?, '')::uuid THEN 0 ELSE 1 END AS known_rank,
			           CASE WHEN name.normalized_name = ? THEN 0 ELSE 1 END AS name_rank
				    FROM entity_records AS rec
				    LEFT JOIN entity_names AS name
				      ON name.team_id = rec.team_id
				     AND name.entity_id = rec.entity_id
				     AND name.valid_to IS NULL
				     AND name.name_kind IN ('canonical', 'alias')
				    LEFT JOIN entity_names AS canonical
				      ON canonical.team_id = rec.team_id
				     AND canonical.entity_id = rec.entity_id
				     AND canonical.name_kind = 'canonical'
				     AND canonical.valid_to IS NULL
				    WHERE rec.team_id = ?::uuid
				      AND rec.status = 'active'
				      AND rec.entity_kind = ?
				      AND (
				          rec.entity_id = NULLIF(?, '')::uuid
			          OR name.normalized_name = ?
				      )
				    ORDER BY rec.entity_id, known_rank, name_rank, name.created_at DESC
				)
				SELECT team_id, entity_id, entity_kind, canonical_name, active_names, identity_context, status
				FROM candidates
				ORDER BY known_rank, name_rank, entity_id
				LIMIT ?
		`, target.KnownEntityID, normalizeName(target.Surface), input.TeamID, target.EntityKind,
				target.KnownEntityID, normalizeName(target.Surface), input.CandidateLimit+1).Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				candidate := SemanticReviewEntityCandidate{}
				var identityRaw []byte
				var activeNames pq.StringArray
				if err := rows.Scan(
					&candidate.TeamID,
					&candidate.EntityID,
					&candidate.EntityKind,
					&candidate.CanonicalName,
					&activeNames,
					&identityRaw,
					&candidate.Status,
				); err != nil {
					_ = rows.Close()
					return err
				}
				if len(group.Candidates) == input.CandidateLimit {
					group.Complete = false
					result.Complete = false
					continue
				}
				if len(identityRaw) > 0 && json.Unmarshal(identityRaw, &candidate.IdentityContext) != nil {
					_ = rows.Close()
					return fmt.Errorf("decode entity identity_context")
				}
				candidate.ActiveNames = append([]string(nil), activeNames...)
				group.Candidates = append(group.Candidates, candidate)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			result.Groups = append(result.Groups, group)
		}
		return nil
	})
	if err != nil {
		return SubmissionAssessmentEntityCatalogResult{}, fmt.Errorf("semantic: list submission assessment entity catalog: %w", err)
	}
	return result, nil
}

func normalizeSubmissionAssessmentEntityCatalogInput(input SubmissionAssessmentEntityCatalogInput) SubmissionAssessmentEntityCatalogInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if input.CandidateLimit <= 0 {
		input.CandidateLimit = submissionAssessmentMaxCandidateSet
	}
	seen := make(map[string]struct{}, len(input.Entities))
	targets := make([]SubmissionAssessmentEntityCatalogTarget, 0, len(input.Entities))
	for _, target := range input.Entities {
		target.Ref = strings.TrimSpace(target.Ref)
		target.Surface = strings.TrimSpace(target.Surface)
		target.EntityKind = strings.TrimSpace(target.EntityKind)
		target.KnownEntityID = strings.TrimSpace(target.KnownEntityID)
		if target.Ref == "" {
			continue
		}
		if _, exists := seen[target.Ref]; exists {
			continue
		}
		seen[target.Ref] = struct{}{}
		targets = append(targets, target)
	}
	input.Entities = targets
	return input
}

func validateSubmissionAssessmentEntityCatalogInput(input SubmissionAssessmentEntityCatalogInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.Entities) == 0 || len(input.Entities) > submissionAssessmentMaxEntityTargets {
		return fmt.Errorf("entities must contain between 1 and %d entries", submissionAssessmentMaxEntityTargets)
	}
	if input.CandidateLimit < 1 || input.CandidateLimit > submissionAssessmentMaxCandidateSet {
		return fmt.Errorf("candidate_limit must be between 1 and %d", submissionAssessmentMaxCandidateSet)
	}
	for _, target := range input.Entities {
		if target.Ref == "" || len([]rune(target.Ref)) > 128 {
			return errors.New("entity ref is required and must be bounded")
		}
		if target.Surface == "" || len([]rune(target.Surface)) > 1000 {
			return errors.New("entity surface is required and must be bounded")
		}
		if !contains(domain.EntityKinds(), target.EntityKind) {
			return fmt.Errorf("entity kind is unsupported %q", target.EntityKind)
		}
		if target.KnownEntityID != "" {
			if _, err := uuid.Parse(target.KnownEntityID); err != nil {
				return fmt.Errorf("known_entity_id is invalid: %w", err)
			}
		}
	}
	return nil
}
