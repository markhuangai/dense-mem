package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	v2SemanticReviewDefaultCandidateLimit = 5
	v2SemanticReviewMaxCandidateLimit     = 20
	v2SemanticReviewDefaultOptionLimit    = 100
	v2SemanticReviewMaxOptionLimit        = 200
)

func (r *V2SemanticRepositoryImpl) ListV2SemanticReviewEntityCandidates(
	ctx context.Context,
	input V2SemanticReviewEntityCandidateInput,
) ([]V2SemanticReviewEntityCandidate, error) {
	input = normalizeV2SemanticReviewEntityCandidateInput(input)
	if err := validateV2SemanticReviewEntityCandidateInput(input); err != nil {
		return nil, err
	}
	if input.Name == "" && input.KnownEntityID == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]V2SemanticReviewEntityCandidate, 0, input.Limit)
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if input.KnownEntityID != "" {
			candidate, err := loadV2SemanticReviewEntityCandidateByID(ctx, tx, input)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if candidate != nil {
				seen[candidate.EntityID] = struct{}{}
				out = append(out, *candidate)
			}
		}
		if input.Name == "" || len(out) >= input.Limit {
			return nil
		}
		candidates, err := loadV2SemanticReviewEntityCandidatesByName(ctx, tx, input, input.Limit-len(out))
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if _, exists := seen[candidate.EntityID]; exists {
				continue
			}
			seen[candidate.EntityID] = struct{}{}
			out = append(out, candidate)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: list review entity candidates: %w", err)
	}
	return out, nil
}

func (r *V2SemanticRepositoryImpl) ListV2SemanticReviewPredicateCandidates(
	ctx context.Context,
	input V2SemanticReviewPredicateCandidateInput,
) ([]V2SemanticReviewPredicateCandidate, error) {
	input = normalizeV2SemanticReviewPredicateCandidateInput(input)
	if err := validateV2SemanticReviewPredicateCandidateInput(input); err != nil {
		return nil, err
	}
	if input.Predicate == "" {
		return nil, nil
	}
	var out []V2SemanticReviewPredicateCandidate
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality, lifecycle_state
			FROM predicate_definitions
			WHERE lifecycle_state = 'active'
			  AND (predicate_key = ? OR ? = ANY(aliases))
			ORDER BY CASE WHEN predicate_key = ? THEN 0 ELSE 1 END, version DESC
			LIMIT ?
		`, input.Predicate, input.Predicate, input.Predicate, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		candidates, err := scanV2SemanticReviewPredicateCandidates(rows)
		if err != nil {
			return err
		}
		out = candidates
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: list review predicate candidates: %w", err)
	}
	return out, nil
}

func (r *V2SemanticRepositoryImpl) ListV2SemanticReviewPredicateOptions(
	ctx context.Context,
	input V2SemanticReviewPredicateOptionsInput,
) ([]string, error) {
	input = normalizeV2SemanticReviewPredicateOptionsInput(input)
	if err := validateV2SemanticReviewPredicateOptionsInput(input); err != nil {
		return nil, err
	}
	var out []string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT predicate_key, aliases
			FROM predicate_definitions
			WHERE lifecycle_state = 'active'
			ORDER BY predicate_key ASC, version DESC
			LIMIT ?
		`, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]struct{}{}
		for rows.Next() {
			var key string
			var aliases pq.StringArray
			if err := rows.Scan(&key, &aliases); err != nil {
				return err
			}
			for _, option := range append([]string{key}, []string(aliases)...) {
				option = strings.TrimSpace(option)
				if option == "" {
					continue
				}
				if _, exists := seen[option]; exists {
					continue
				}
				seen[option] = struct{}{}
				out = append(out, option)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: list review predicate options: %w", err)
	}
	return out, nil
}

func normalizeV2SemanticReviewEntityCandidateInput(input V2SemanticReviewEntityCandidateInput) V2SemanticReviewEntityCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Name = strings.TrimSpace(input.Name)
	input.EntityKind = strings.TrimSpace(input.EntityKind)
	input.KnownEntityID = strings.TrimSpace(input.KnownEntityID)
	input.Limit = normalizeV2ReviewCandidateLimit(input.Limit)
	return input
}

func validateV2SemanticReviewEntityCandidateInput(input V2SemanticReviewEntityCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.KnownEntityID != "" {
		if _, err := uuid.Parse(input.KnownEntityID); err != nil {
			return fmt.Errorf("known_entity_id is invalid: %w", err)
		}
	}
	return nil
}

func normalizeV2SemanticReviewPredicateCandidateInput(input V2SemanticReviewPredicateCandidateInput) V2SemanticReviewPredicateCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Predicate = strings.TrimSpace(input.Predicate)
	input.Limit = normalizeV2ReviewCandidateLimit(input.Limit)
	return input
}

func validateV2SemanticReviewPredicateCandidateInput(input V2SemanticReviewPredicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func normalizeV2SemanticReviewPredicateOptionsInput(input V2SemanticReviewPredicateOptionsInput) V2SemanticReviewPredicateOptionsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Limit = normalizeV2ReviewOptionLimit(input.Limit)
	return input
}

func validateV2SemanticReviewPredicateOptionsInput(input V2SemanticReviewPredicateOptionsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func normalizeV2ReviewCandidateLimit(limit int) int {
	if limit <= 0 {
		return v2SemanticReviewDefaultCandidateLimit
	}
	if limit > v2SemanticReviewMaxCandidateLimit {
		return v2SemanticReviewMaxCandidateLimit
	}
	return limit
}

func normalizeV2ReviewOptionLimit(limit int) int {
	if limit <= 0 {
		return v2SemanticReviewDefaultOptionLimit
	}
	if limit > v2SemanticReviewMaxOptionLimit {
		return v2SemanticReviewMaxOptionLimit
	}
	return limit
}

func loadV2SemanticReviewEntityCandidateByID(
	ctx context.Context,
	tx *gorm.DB,
	input V2SemanticReviewEntityCandidateInput,
) (*V2SemanticReviewEntityCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT rec.team_id::text, rec.entity_id::text, rec.entity_kind,
		       COALESCE(canonical.display_name, ''), rec.identity_context, rec.status
		FROM entity_records AS rec
		LEFT JOIN entity_names AS canonical
		  ON canonical.team_id = rec.team_id
		 AND canonical.entity_id = rec.entity_id
		 AND canonical.name_kind = 'canonical'
		 AND canonical.valid_to IS NULL
		WHERE rec.team_id = ?::uuid
		  AND rec.entity_id = ?::uuid
		  AND (? = '' OR rec.entity_kind = ?)
		LIMIT 1
	`, input.TeamID, input.KnownEntityID, input.EntityKind, input.EntityKind).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, err := scanV2SemanticReviewEntityCandidates(rows)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, sql.ErrNoRows
	}
	return &candidates[0], rows.Err()
}

func loadV2SemanticReviewEntityCandidatesByName(
	ctx context.Context,
	tx *gorm.DB,
	input V2SemanticReviewEntityCandidateInput,
	limit int,
) ([]V2SemanticReviewEntityCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (rec.entity_id)
		       rec.team_id::text, rec.entity_id::text, rec.entity_kind,
		       COALESCE(canonical.display_name, name.display_name), rec.identity_context, rec.status
		FROM entity_names AS name
		JOIN entity_records AS rec
		  ON rec.team_id = name.team_id
		 AND rec.entity_id = name.entity_id
		LEFT JOIN entity_names AS canonical
		  ON canonical.team_id = rec.team_id
		 AND canonical.entity_id = rec.entity_id
		 AND canonical.name_kind = 'canonical'
		 AND canonical.valid_to IS NULL
		WHERE name.team_id = ?::uuid
		  AND name.normalized_name = ?
		  AND name.valid_to IS NULL
		  AND (? = '' OR rec.entity_kind = ?)
		ORDER BY rec.entity_id, CASE WHEN name.name_kind = 'canonical' THEN 0 ELSE 1 END, name.created_at DESC
		LIMIT ?
	`, input.TeamID, normalizeV2Name(input.Name), input.EntityKind, input.EntityKind, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, err := scanV2SemanticReviewEntityCandidates(rows)
	if err != nil {
		return nil, err
	}
	return candidates, rows.Err()
}

func scanV2SemanticReviewEntityCandidates(rows *sql.Rows) ([]V2SemanticReviewEntityCandidate, error) {
	out := []V2SemanticReviewEntityCandidate{}
	for rows.Next() {
		var candidate V2SemanticReviewEntityCandidate
		var contextRaw []byte
		if err := rows.Scan(
			&candidate.TeamID,
			&candidate.EntityID,
			&candidate.EntityKind,
			&candidate.CanonicalName,
			&contextRaw,
			&candidate.Status,
		); err != nil {
			return nil, err
		}
		if len(contextRaw) > 0 {
			if err := json.Unmarshal(contextRaw, &candidate.IdentityContext); err != nil {
				return nil, err
			}
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func scanV2SemanticReviewPredicateCandidates(rows *sql.Rows) ([]V2SemanticReviewPredicateCandidate, error) {
	out := []V2SemanticReviewPredicateCandidate{}
	for rows.Next() {
		var candidate V2SemanticReviewPredicateCandidate
		var subjectKinds pq.StringArray
		var objectKinds pq.StringArray
		if err := rows.Scan(
			&candidate.PredicateKey,
			&candidate.Version,
			&subjectKinds,
			&objectKinds,
			&candidate.RelationshipKind,
			&candidate.CurrentCardinality,
			&candidate.LifecycleState,
		); err != nil {
			return nil, err
		}
		candidate.AllowedSubjectKinds = []string(subjectKinds)
		candidate.AllowedObjectKinds = []string(objectKinds)
		out = append(out, candidate)
	}
	return out, rows.Err()
}
