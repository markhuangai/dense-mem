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
	submissionAssessmentMaxPredicateSet  = 2000
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
				           rec.identity_context, rec.status,
				           CASE WHEN rec.entity_id = NULLIF(?, '')::uuid THEN 0 ELSE 1 END AS known_rank,
				           CASE WHEN lower(COALESCE(name.display_name, '')) = lower(?) THEN 0 ELSE 1 END AS name_rank
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
				          OR lower(COALESCE(name.display_name, '')) = lower(?)
				      )
				    ORDER BY rec.entity_id, known_rank, name_rank, name.created_at DESC
				)
				SELECT team_id, entity_id, entity_kind, canonical_name, identity_context, status
				FROM candidates
				ORDER BY known_rank, name_rank, entity_id
				LIMIT ?
			`, target.KnownEntityID, target.Surface, input.TeamID, target.EntityKind,
				target.KnownEntityID, target.Surface, input.CandidateLimit+1).Rows()
			if err != nil {
				return err
			}
			for rows.Next() {
				candidate := SemanticReviewEntityCandidate{}
				var identityRaw []byte
				if err := rows.Scan(
					&candidate.TeamID,
					&candidate.EntityID,
					&candidate.EntityKind,
					&candidate.CanonicalName,
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

func (r *SemanticRepositoryImpl) ListSubmissionAssessmentPredicateCatalog(
	ctx context.Context,
	input SubmissionAssessmentPredicateCatalogInput,
) (SubmissionAssessmentPredicateCatalogResult, error) {
	input = normalizeSubmissionAssessmentPredicateCatalogInput(input)
	if err := validateSubmissionAssessmentPredicateCatalogInput(input); err != nil {
		return SubmissionAssessmentPredicateCatalogResult{}, err
	}
	result := SubmissionAssessmentPredicateCatalogResult{Options: []SemanticReviewPredicateCandidate{}, Complete: true}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH latest AS (
			    SELECT DISTINCT ON (predicate_key)
			           predicate_key, version, aliases, allowed_subject_kinds,
			           allowed_object_kinds, relationship_kind, current_cardinality,
			           lifecycle_state, origin, created_at
			    FROM team_predicate_definitions
			    WHERE team_id = ?::uuid
			    ORDER BY predicate_key, version DESC
			)
			SELECT predicate_key, version, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality,
			       lifecycle_state
			FROM latest
			WHERE lifecycle_state = 'active'
			ORDER BY CASE WHEN origin = 'built_in' THEN 0 ELSE 1 END,
			         created_at ASC,
			         predicate_key ASC
			LIMIT ?
		`, input.TeamID, input.Limit+1).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			candidate := SemanticReviewPredicateCandidate{}
			var aliases, subjectKinds, objectKinds pq.StringArray
			if err := rows.Scan(
				&candidate.PredicateKey,
				&candidate.Version,
				&aliases,
				&subjectKinds,
				&objectKinds,
				&candidate.RelationshipKind,
				&candidate.CurrentCardinality,
				&candidate.LifecycleState,
			); err != nil {
				return err
			}
			if len(result.Options) == input.Limit {
				result.Complete = false
				continue
			}
			candidate.Aliases = []string(aliases)
			candidate.AllowedSubjectKinds = []string(subjectKinds)
			candidate.AllowedObjectKinds = []string(objectKinds)
			result.Options = append(result.Options, candidate)
		}
		return rows.Err()
	})
	if err != nil {
		return SubmissionAssessmentPredicateCatalogResult{}, fmt.Errorf("semantic: list submission assessment predicate catalog: %w", err)
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

func normalizeSubmissionAssessmentPredicateCatalogInput(input SubmissionAssessmentPredicateCatalogInput) SubmissionAssessmentPredicateCatalogInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if input.Limit <= 0 {
		input.Limit = semanticReviewDefaultOptionLimit
	}
	return input
}

func validateSubmissionAssessmentPredicateCatalogInput(input SubmissionAssessmentPredicateCatalogInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.Limit < 1 || input.Limit > submissionAssessmentMaxPredicateSet {
		return fmt.Errorf("limit must be between 1 and %d", submissionAssessmentMaxPredicateSet)
	}
	return nil
}
