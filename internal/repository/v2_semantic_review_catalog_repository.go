package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	v2SemanticReviewDefaultCandidateLimit = 5
	v2SemanticReviewMaxCandidateLimit     = 20
	v2SemanticReviewDefaultOptionLimit    = 100
	v2SemanticReviewMaxOptionLimit        = 100
	v2SemanticReviewMaxResolutionInputs   = 200
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
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
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
			WITH latest AS (
			    SELECT predicate_key, version, aliases,
			           allowed_subject_kinds, allowed_object_kinds,
			           relationship_kind, current_cardinality, lifecycle_state,
			           row_number() OVER (
			               PARTITION BY predicate_key
			               ORDER BY version DESC
			           ) AS version_rank
			    FROM team_predicate_definitions
			    WHERE team_id = ?::uuid
			)
			SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality, lifecycle_state
			FROM latest
			WHERE version_rank = 1
			  AND lifecycle_state = 'active'
			  AND (predicate_key = ? OR ? = ANY(aliases))
			ORDER BY CASE WHEN predicate_key = ? THEN 0 ELSE 1 END, version DESC
			LIMIT ?
		`, input.TeamID, input.Predicate, input.Predicate, input.Predicate, input.Limit).Rows()
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

func (r *V2SemanticRepositoryImpl) ResolveV2SemanticReviewPredicateCandidates(
	ctx context.Context,
	input V2SemanticReviewPredicateResolutionInput,
) ([]V2SemanticReviewPredicateResolution, error) {
	input = normalizeV2SemanticReviewPredicateResolutionInput(input)
	if err := validateV2SemanticReviewPredicateResolutionInput(input); err != nil {
		return nil, err
	}
	if len(input.Predicates) == 0 {
		return nil, nil
	}
	normalizedPredicates := make([]string, 0, len(input.Predicates))
	for _, predicate := range input.Predicates {
		normalizedPredicates = append(normalizedPredicates, canonicalV2GeneratedPredicateKey(predicate))
	}
	out := []V2SemanticReviewPredicateResolution{}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH requested AS (
			    SELECT btrim(input.requested_predicate) AS requested_predicate,
			           input.normalized_predicate,
			           input.ordinality AS requested_order
			    FROM unnest(?::text[], ?::text[]) WITH ORDINALITY
			         AS input(requested_predicate, normalized_predicate, ordinality)
			    WHERE btrim(input.requested_predicate) <> ''
			), latest_definitions AS (
			    SELECT definition.*,
			           row_number() OVER (
			               PARTITION BY definition.predicate_key
			               ORDER BY definition.version DESC
			           ) AS version_rank
			    FROM team_predicate_definitions AS definition
			    WHERE definition.team_id = ?::uuid
			), matched AS (
			    SELECT requested.requested_predicate, requested.requested_order,
			           CASE WHEN definition.predicate_key = requested.normalized_predicate
			                THEN 'key' ELSE 'alias' END AS match_kind,
			           definition.predicate_key, definition.version,
			           definition.allowed_subject_kinds, definition.allowed_object_kinds,
			           definition.relationship_kind, definition.current_cardinality,
			           definition.lifecycle_state
			    FROM requested
			    JOIN latest_definitions AS definition
			      ON definition.version_rank = 1
			     AND definition.lifecycle_state = 'active'
			     AND (
			         definition.predicate_key = requested.normalized_predicate
			         OR requested.requested_predicate = ANY(definition.aliases)
			         OR requested.normalized_predicate = ANY(definition.aliases)
			     )
			), latest AS (
			    SELECT *,
			           row_number() OVER (
			               PARTITION BY requested_order
			               ORDER BY CASE match_kind WHEN 'key' THEN 0 ELSE 1 END,
			                        predicate_key
			           ) AS match_rank
			    FROM matched
			)
			SELECT requested_predicate, match_kind, predicate_key, version,
			       allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality, lifecycle_state
			FROM latest
			WHERE match_rank <= ?
			ORDER BY requested_order, match_rank
		`, pq.Array(input.Predicates), pq.Array(normalizedPredicates), input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var resolution V2SemanticReviewPredicateResolution
			var subjectKinds pq.StringArray
			var objectKinds pq.StringArray
			if err := rows.Scan(
				&resolution.RequestedPredicate,
				&resolution.MatchKind,
				&resolution.Candidate.PredicateKey,
				&resolution.Candidate.Version,
				&subjectKinds,
				&objectKinds,
				&resolution.Candidate.RelationshipKind,
				&resolution.Candidate.CurrentCardinality,
				&resolution.Candidate.LifecycleState,
			); err != nil {
				return err
			}
			resolution.Candidate.AllowedSubjectKinds = []string(subjectKinds)
			resolution.Candidate.AllowedObjectKinds = []string(objectKinds)
			out = append(out, resolution)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: resolve review predicate candidates: %w", err)
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
			WITH latest AS (
			    SELECT DISTINCT ON (predicate_key)
			           predicate_key, aliases, lifecycle_state, origin, created_at
			    FROM team_predicate_definitions
			    WHERE team_id = ?::uuid
			    ORDER BY predicate_key, version DESC
			), evidence_terms AS (
			    SELECT DISTINCT evidence_term.term
			    FROM unnest(tsvector_to_array(to_tsvector('english', ?)))
			         AS evidence_term(term)
			), ranked AS (
			    SELECT latest.*,
			           (
			               SELECT count(*)
			               FROM unnest(tsvector_to_array(to_tsvector(
			                   'english',
			                   replace(latest.predicate_key, '_', ' ') || ' ' ||
			                   array_to_string(latest.aliases, ' ')
			               ))) AS predicate_term(term)
			               JOIN evidence_terms
			                 ON evidence_terms.term = predicate_term.term
			           ) AS relevance
			    FROM latest
			    WHERE lifecycle_state = 'active'
			)
			SELECT predicate_key, aliases
			FROM ranked
			ORDER BY relevance DESC,
			         CASE WHEN origin = 'built_in' THEN 0 ELSE 1 END,
			         created_at DESC,
			         predicate_key
			LIMIT ?
		`, input.TeamID, input.QueryText, input.Limit).Rows()
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
				if len(out) == input.Limit {
					break
				}
			}
			if len(out) == input.Limit {
				break
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: list review predicate options: %w", err)
	}
	return out, nil
}

func (r *V2SemanticRepositoryImpl) EnsureV2SemanticReviewPredicateCandidate(
	ctx context.Context,
	input V2EnsureSemanticPredicateCandidateInput,
) (*V2SemanticReviewPredicateCandidate, error) {
	input = normalizeV2EnsureSemanticPredicateCandidateInput(input)
	if err := validateV2EnsureSemanticPredicateCandidateInput(input); err != nil {
		return nil, err
	}
	var out *V2SemanticReviewPredicateCandidate
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		if err := seedV2TeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		candidate, err := ensureV2SemanticPredicateCandidateTx(ctx, tx, input)
		if err != nil {
			return err
		}
		out = candidate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic: ensure review predicate candidate: %w", err)
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

func normalizeV2SemanticReviewPredicateResolutionInput(input V2SemanticReviewPredicateResolutionInput) V2SemanticReviewPredicateResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Limit = normalizeV2ReviewCandidateLimit(input.Limit)
	seen := map[string]struct{}{}
	predicates := make([]string, 0, len(input.Predicates))
	for _, predicate := range input.Predicates {
		predicate = strings.TrimSpace(predicate)
		if predicate == "" {
			continue
		}
		if _, exists := seen[predicate]; exists {
			continue
		}
		seen[predicate] = struct{}{}
		predicates = append(predicates, predicate)
		if len(predicates) == v2SemanticReviewMaxResolutionInputs {
			break
		}
	}
	input.Predicates = predicates
	return input
}

func validateV2SemanticReviewPredicateResolutionInput(input V2SemanticReviewPredicateResolutionInput) error {
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
	input.QueryText = strings.TrimSpace(input.QueryText)
	queryRunes := []rune(input.QueryText)
	if len(queryRunes) > 32000 {
		input.QueryText = string(queryRunes[:32000])
	}
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

func normalizeV2EnsureSemanticPredicateCandidateInput(input V2EnsureSemanticPredicateCandidateInput) V2EnsureSemanticPredicateCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Predicate = strings.TrimSpace(input.Predicate)
	input.RelationshipKind = strings.TrimSpace(input.RelationshipKind)
	input.SubjectKind = strings.TrimSpace(input.SubjectKind)
	input.ObjectKind = strings.TrimSpace(input.ObjectKind)
	input.Origin = strings.TrimSpace(input.Origin)
	if input.Origin == "" {
		input.Origin = "provider_generated"
	}
	if input.SubjectKind == "" {
		input.SubjectKind = string(domain.V2EntityKindOther)
	}
	if input.ObjectKind == "" {
		input.ObjectKind = string(domain.V2EntityKindOther)
	}
	return input
}

func validateV2EnsureSemanticPredicateCandidateInput(input V2EnsureSemanticPredicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.Predicate == "" {
		return errors.New("predicate is required")
	}
	if !v2Contains(domain.V2RelationshipKinds(), input.RelationshipKind) {
		return fmt.Errorf("relationship_kind is unsupported %q", input.RelationshipKind)
	}
	if !v2Contains(domain.V2EntityKinds(), input.SubjectKind) {
		return fmt.Errorf("subject_kind is unsupported %q", input.SubjectKind)
	}
	if !v2Contains(append(domain.V2EntityKinds(), domain.V2ValueTypes()...), input.ObjectKind) {
		return fmt.Errorf("object_kind is unsupported %q", input.ObjectKind)
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

func seedV2TeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata, created_at
		)
		SELECT ?::uuid, predicate_key, version, aliases, allowed_subject_kinds,
		       allowed_object_kinds, relationship_kind, current_cardinality,
		       lifecycle_state, 'built_in',
		       metadata || jsonb_build_object('source', 'predicate_definitions'),
		       created_at
		FROM predicate_definitions
		ON CONFLICT (team_id, predicate_key, version) DO NOTHING
	`, teamID).Error
}

func ensureV2SemanticPredicateCandidateTx(
	ctx context.Context,
	tx *gorm.DB,
	input V2EnsureSemanticPredicateCandidateInput,
) (*V2SemanticReviewPredicateCandidate, error) {
	baseKey := canonicalV2GeneratedPredicateKey(input.Predicate)
	if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+baseKey).Error; err != nil {
		return nil, err
	}
	candidate, err := loadV2TeamPredicateCandidateByKeyOrAlias(ctx, tx, input.TeamID, baseKey, input.Predicate)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if candidate != nil && candidate.RelationshipKind == input.RelationshipKind {
		return ensureV2TeamPredicateCandidateKinds(ctx, tx, input, *candidate)
	}
	key := baseKey
	collision := candidate != nil && candidate.RelationshipKind != input.RelationshipKind
	if collision {
		key = collisionV2GeneratedPredicateKey(baseKey, input.RelationshipKind, input.Predicate)
		if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+key).Error; err != nil {
			return nil, err
		}
		candidate, err = loadV2TeamPredicateCandidateByExactKey(ctx, tx, input.TeamID, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if candidate != nil {
			if candidate.RelationshipKind != input.RelationshipKind {
				return nil, fmt.Errorf("predicate key collision %q has incompatible relationship_kind %q", key, candidate.RelationshipKind)
			}
			return ensureV2TeamPredicateCandidateKinds(ctx, tx, input, *candidate)
		}
	}
	return insertV2TeamPredicateCandidate(ctx, tx, input, key, collision)
}

func loadV2TeamPredicateCandidateByKeyOrAlias(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	key string,
	alias string,
) (*V2SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality, lifecycle_state
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND lifecycle_state = 'active'
		  AND (predicate_key = ? OR ? = ANY(aliases))
		ORDER BY CASE WHEN predicate_key = ? THEN 0 ELSE 1 END, version DESC
		LIMIT 1
	`, teamID, key, alias, key).Rows()
	if err != nil {
		return nil, err
	}
	return scanOneV2TeamPredicateCandidate(rows)
}

func loadV2TeamPredicateCandidateByExactKey(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	key string,
) (*V2SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality, lifecycle_state
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND predicate_key = ?
		  AND lifecycle_state = 'active'
		ORDER BY version DESC
		LIMIT 1
	`, teamID, key).Rows()
	if err != nil {
		return nil, err
	}
	return scanOneV2TeamPredicateCandidate(rows)
}

func ensureV2TeamPredicateCandidateKinds(
	ctx context.Context,
	tx *gorm.DB,
	input V2EnsureSemanticPredicateCandidateInput,
	candidate V2SemanticReviewPredicateCandidate,
) (*V2SemanticReviewPredicateCandidate, error) {
	subjectKinds := unionV2StringSet(candidate.AllowedSubjectKinds, []string{input.SubjectKind})
	objectKinds := unionV2StringSet(candidate.AllowedObjectKinds, []string{input.ObjectKind})
	if len(subjectKinds) == len(candidate.AllowedSubjectKinds) && len(objectKinds) == len(candidate.AllowedObjectKinds) {
		return &candidate, nil
	}
	metadata := map[string]any{
		"source":             "generated_predicate_expansion",
		"previous_version":   candidate.Version,
		"original_predicate": input.Predicate,
	}
	for key, value := range input.Metadata {
		metadata[key] = value
	}
	data, err := marshalV2JSON(metadata)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata
		) VALUES (
		    ?::uuid, ?, ?, ARRAY[]::text[], ?, ?, ?, ?, 'active', ?, ?::jsonb
		)
		RETURNING predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		          relationship_kind, current_cardinality, lifecycle_state
	`, input.TeamID, candidate.PredicateKey, candidate.Version+1, pqStringArray(subjectKinds),
		pqStringArray(objectKinds), candidate.RelationshipKind, candidate.CurrentCardinality,
		input.Origin, string(data)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, err := scanOneV2TeamPredicateCandidate(rows)
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func insertV2TeamPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	input V2EnsureSemanticPredicateCandidateInput,
	key string,
	collision bool,
) (*V2SemanticReviewPredicateCandidate, error) {
	metadata := map[string]any{
		"source":             "generated_predicate",
		"original_predicate": input.Predicate,
	}
	for name, value := range input.Metadata {
		metadata[name] = value
	}
	data, err := marshalV2JSON(metadata)
	if err != nil {
		return nil, err
	}
	aliases := []string{}
	if !collision {
		aliases = unionV2StringSet(aliases, []string{input.Predicate})
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata
		) VALUES (
		    ?::uuid, ?, 1, ?, ?, ?, ?, 'many', 'active', ?, ?::jsonb
		)
		ON CONFLICT (team_id, predicate_key, version) DO NOTHING
		RETURNING predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		          relationship_kind, current_cardinality, lifecycle_state
	`, input.TeamID, key, pqStringArray(aliases), pqStringArray([]string{input.SubjectKind}),
		pqStringArray([]string{input.ObjectKind}), input.RelationshipKind, input.Origin,
		string(data)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, err := scanOneV2TeamPredicateCandidate(rows)
	if errors.Is(err, sql.ErrNoRows) {
		return loadV2TeamPredicateCandidateByExactKey(ctx, tx, input.TeamID, key)
	}
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func scanOneV2TeamPredicateCandidate(rows *sql.Rows) (*V2SemanticReviewPredicateCandidate, error) {
	defer rows.Close()
	candidates, err := scanV2SemanticReviewPredicateCandidates(rows)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, sql.ErrNoRows
	}
	return &candidates[0], rows.Err()
}

func canonicalV2GeneratedPredicateKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out []rune
	lastUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
			lastUnderscore = false
			continue
		}
		if len(out) == 0 || lastUnderscore {
			continue
		}
		out = append(out, '_')
		lastUnderscore = true
	}
	for len(out) > 0 && out[len(out)-1] == '_' {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
		for len(out) > 0 && out[len(out)-1] == '_' {
			out = out[:len(out)-1]
		}
	}
	if len(out) == 0 {
		return "predicate_" + shortV2PredicateHash(value, 12)
	}
	return string(out)
}

func collisionV2GeneratedPredicateKey(base string, relationshipKind string, original string) string {
	suffix := "__" + strings.TrimSpace(relationshipKind) + "_" + shortV2PredicateHash(original+":"+relationshipKind, 8)
	runes := []rune(base)
	maxBase := 64 - len([]rune(suffix))
	if maxBase < 1 {
		maxBase = 1
	}
	if len(runes) > maxBase {
		runes = runes[:maxBase]
	}
	for len(runes) > 0 && runes[len(runes)-1] == '_' {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		runes = []rune("predicate")
	}
	return string(runes) + suffix
}

func shortV2PredicateHash(value string, n int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if n <= 0 || n > len(encoded) {
		return encoded
	}
	return encoded[:n]
}

func unionV2StringSet(left []string, right []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(left)+len(right))
	for _, value := range append(left, right...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
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
