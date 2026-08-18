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
	"github.com/markhuangai/dense-mem/internal/postgrescompat"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	semanticReviewDefaultCandidateLimit = 5
	semanticReviewMaxCandidateLimit     = 20
	semanticReviewDefaultOptionLimit    = 100
	semanticReviewMaxOptionLimit        = 100
	semanticReviewMaxResolutionLimit    = semanticReviewMaxOptionLimit + 1
	semanticReviewMaxResolutionInputs   = 200
)

func (r *SemanticRepositoryImpl) ListSemanticReviewEntityCandidates(
	ctx context.Context,
	input SemanticReviewEntityCandidateInput,
) ([]SemanticReviewEntityCandidate, error) {
	input = normalizeSemanticReviewEntityCandidateInput(input)
	if err := validateSemanticReviewEntityCandidateInput(input); err != nil {
		return nil, err
	}
	if input.Name == "" && input.KnownEntityID == "" {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]SemanticReviewEntityCandidate, 0, input.Limit)
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if input.KnownEntityID != "" {
			candidate, err := loadSemanticReviewEntityCandidateByID(ctx, tx, input)
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
		candidates, err := loadSemanticReviewEntityCandidatesByName(ctx, tx, input, input.Limit-len(out))
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
		return nil, fmt.Errorf("semantic: list review entity candidates: %w", err)
	}
	return out, nil
}

func (r *SemanticRepositoryImpl) ListSemanticReviewPredicateCandidates(
	ctx context.Context,
	input SemanticReviewPredicateCandidateInput,
) ([]SemanticReviewPredicateCandidate, error) {
	input = normalizeSemanticReviewPredicateCandidateInput(input)
	if err := validateSemanticReviewPredicateCandidateInput(input); err != nil {
		return nil, err
	}
	if input.Predicate == "" {
		return nil, nil
	}
	var out []SemanticReviewPredicateCandidate
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
		candidates, err := scanSemanticReviewPredicateCandidates(rows)
		if err != nil {
			return err
		}
		out = candidates
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: list review predicate candidates: %w", err)
	}
	return out, nil
}

func (r *SemanticRepositoryImpl) ResolveSemanticReviewPredicateCandidates(
	ctx context.Context,
	input SemanticReviewPredicateResolutionInput,
) ([]SemanticReviewPredicateResolution, error) {
	input = normalizeSemanticReviewPredicateResolutionInput(input)
	if err := validateSemanticReviewPredicateResolutionInput(input); err != nil {
		return nil, err
	}
	if len(input.Predicates) == 0 {
		return nil, nil
	}
	normalizedPredicates := make([]string, 0, len(input.Predicates))
	for _, predicate := range input.Predicates {
		normalizedPredicates = append(normalizedPredicates, canonicalGeneratedPredicateKey(predicate))
	}
	out := []SemanticReviewPredicateResolution{}
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
			           definition.predicate_key, definition.version, definition.aliases,
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
			SELECT requested_predicate, match_kind, predicate_key, version, aliases,
			       allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality, lifecycle_state
			FROM latest
			WHERE match_rank <= ?
			ORDER BY requested_order, match_rank
		`, postgrescompat.Array(input.Predicates), postgrescompat.Array(normalizedPredicates), input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var resolution SemanticReviewPredicateResolution
			var aliases postgrescompat.StringArray
			var subjectKinds postgrescompat.StringArray
			var objectKinds postgrescompat.StringArray
			if err := rows.Scan(
				&resolution.RequestedPredicate,
				&resolution.MatchKind,
				&resolution.Candidate.PredicateKey,
				&resolution.Candidate.Version,
				&aliases,
				&subjectKinds,
				&objectKinds,
				&resolution.Candidate.RelationshipKind,
				&resolution.Candidate.CurrentCardinality,
				&resolution.Candidate.LifecycleState,
			); err != nil {
				return err
			}
			resolution.Candidate.Aliases = []string(aliases)
			resolution.Candidate.AllowedSubjectKinds = []string(subjectKinds)
			resolution.Candidate.AllowedObjectKinds = []string(objectKinds)
			out = append(out, resolution)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: resolve review predicate candidates: %w", err)
	}
	return out, nil
}

func (r *SemanticRepositoryImpl) ListSemanticReviewPredicateOptions(
	ctx context.Context,
	input SemanticReviewPredicateOptionsInput,
) ([]string, error) {
	input = normalizeSemanticReviewPredicateOptionsInput(input)
	if err := validateSemanticReviewPredicateOptionsInput(input); err != nil {
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
			var aliases postgrescompat.StringArray
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
		return nil, fmt.Errorf("semantic: list review predicate options: %w", err)
	}
	return out, nil
}

func (r *SemanticRepositoryImpl) EnsureSemanticReviewPredicateCandidate(
	ctx context.Context,
	input EnsureSemanticPredicateCandidateInput,
) (*SemanticReviewPredicateCandidate, error) {
	input = normalizeEnsureSemanticPredicateCandidateInput(input)
	if err := validateEnsureSemanticPredicateCandidateInput(input); err != nil {
		return nil, err
	}
	var out *SemanticReviewPredicateCandidate
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		candidate, err := ensureSemanticPredicateCandidateTx(ctx, tx, input)
		if err != nil {
			return err
		}
		out = candidate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: ensure review predicate candidate: %w", err)
	}
	return out, nil
}

func normalizeSemanticReviewEntityCandidateInput(input SemanticReviewEntityCandidateInput) SemanticReviewEntityCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Name = strings.TrimSpace(input.Name)
	input.EntityKind = strings.TrimSpace(input.EntityKind)
	input.KnownEntityID = strings.TrimSpace(input.KnownEntityID)
	input.Limit = normalizeReviewCandidateLimit(input.Limit)
	return input
}

func normalizeSemanticAssessmentEntityMatchInput(input SemanticAssessmentEntityMatchInput) SemanticAssessmentEntityMatchInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.EvidenceText = strings.TrimSpace(input.EvidenceText)
	runes := []rune(input.EvidenceText)
	if len(runes) > 32000 {
		input.EvidenceText = string(runes[:32000])
	}
	if input.Limit <= 0 {
		input.Limit = 500
	}
	if input.Limit > 1000 {
		input.Limit = 1000
	}
	return input
}

func validateSemanticAssessmentEntityMatchInput(input SemanticAssessmentEntityMatchInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func validateSemanticReviewEntityCandidateInput(input SemanticReviewEntityCandidateInput) error {
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

func normalizeSemanticReviewPredicateCandidateInput(input SemanticReviewPredicateCandidateInput) SemanticReviewPredicateCandidateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Predicate = strings.TrimSpace(input.Predicate)
	input.Limit = normalizeReviewCandidateLimit(input.Limit)
	return input
}

func validateSemanticReviewPredicateCandidateInput(input SemanticReviewPredicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func normalizeSemanticReviewPredicateResolutionInput(input SemanticReviewPredicateResolutionInput) SemanticReviewPredicateResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Limit = normalizeReviewResolutionLimit(input.Limit)
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
		if len(predicates) == semanticReviewMaxResolutionInputs {
			break
		}
	}
	input.Predicates = predicates
	return input
}

func normalizeReviewResolutionLimit(limit int) int {
	if limit <= 0 {
		return semanticReviewDefaultCandidateLimit
	}
	if limit > semanticReviewMaxResolutionLimit {
		return semanticReviewMaxResolutionLimit
	}
	return limit
}

func validateSemanticReviewPredicateResolutionInput(input SemanticReviewPredicateResolutionInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func normalizeSemanticReviewPredicateOptionsInput(input SemanticReviewPredicateOptionsInput) SemanticReviewPredicateOptionsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.QueryText = strings.TrimSpace(input.QueryText)
	queryRunes := []rune(input.QueryText)
	if len(queryRunes) > 32000 {
		input.QueryText = string(queryRunes[:32000])
	}
	input.Limit = normalizeReviewOptionLimit(input.Limit)
	return input
}

func normalizeSemanticAssessmentPredicateOptionsInput(input SemanticAssessmentPredicateOptionsInput) SemanticAssessmentPredicateOptionsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.QueryText = strings.TrimSpace(input.QueryText)
	queryRunes := []rune(input.QueryText)
	if len(queryRunes) > 32000 {
		input.QueryText = string(queryRunes[:32000])
	}
	seen := make(map[string]struct{}, len(input.ProposedKeys))
	proposedKeys := make([]string, 0, len(input.ProposedKeys))
	for _, key := range input.ProposedKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		proposedKeys = append(proposedKeys, key)
	}
	input.ProposedKeys = proposedKeys
	input.Limit = normalizeReviewOptionLimit(input.Limit)
	return input
}

func validateSemanticAssessmentPredicateOptionsInput(input SemanticAssessmentPredicateOptionsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.ProposedKeys) > 200 {
		return fmt.Errorf("proposed_keys must contain at most 200 entries")
	}
	for _, key := range input.ProposedKeys {
		if len([]rune(key)) > 128 {
			return fmt.Errorf("proposed_key must be at most 128 characters")
		}
	}
	return nil
}

func validateSemanticReviewPredicateOptionsInput(input SemanticReviewPredicateOptionsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	return nil
}

func normalizeEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) EnsureSemanticPredicateCandidateInput {
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
		input.SubjectKind = string(domain.EntityKindOther)
	}
	if input.ObjectKind == "" {
		input.ObjectKind = string(domain.EntityKindOther)
	}
	return input
}

func validateEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.Predicate == "" {
		return errors.New("predicate is required")
	}
	if !contains(domain.RelationshipKinds(), input.RelationshipKind) {
		return fmt.Errorf("relationship_kind is unsupported %q", input.RelationshipKind)
	}
	if !contains(domain.EntityKinds(), input.SubjectKind) {
		return fmt.Errorf("subject_kind is unsupported %q", input.SubjectKind)
	}
	if !contains(append(domain.EntityKinds(), domain.ValueTypes()...), input.ObjectKind) {
		return fmt.Errorf("object_kind is unsupported %q", input.ObjectKind)
	}
	return nil
}

func normalizeReviewCandidateLimit(limit int) int {
	if limit <= 0 {
		return semanticReviewDefaultCandidateLimit
	}
	if limit > semanticReviewMaxCandidateLimit {
		return semanticReviewMaxCandidateLimit
	}
	return limit
}

func seedTeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
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

func ensureSemanticPredicateCandidateTx(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureSemanticPredicateCandidateInput,
) (*SemanticReviewPredicateCandidate, error) {
	baseKey := canonicalGeneratedPredicateKey(input.Predicate)
	if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+baseKey).Error; err != nil {
		return nil, err
	}
	candidate, err := loadTeamPredicateCandidateByKeyOrAlias(ctx, tx, input.TeamID, baseKey, input.Predicate)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if candidate != nil && candidate.RelationshipKind == input.RelationshipKind {
		return ensureTeamPredicateCandidateKinds(ctx, tx, input, *candidate)
	}
	key := baseKey
	collision := candidate != nil && candidate.RelationshipKind != input.RelationshipKind
	if collision {
		key = collisionGeneratedPredicateKey(baseKey, input.RelationshipKind, input.Predicate)
		if err := tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`, input.TeamID+":"+key).Error; err != nil {
			return nil, err
		}
		candidate, err = loadTeamPredicateCandidateByExactKey(ctx, tx, input.TeamID, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if candidate != nil {
			if candidate.RelationshipKind != input.RelationshipKind {
				return nil, fmt.Errorf("predicate key collision %q has incompatible relationship_kind %q", key, candidate.RelationshipKind)
			}
			return ensureTeamPredicateCandidateKinds(ctx, tx, input, *candidate)
		}
	}
	return insertTeamPredicateCandidate(ctx, tx, input, key, collision)
}

func loadTeamPredicateCandidateByKeyOrAlias(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	key string,
	alias string,
) (*SemanticReviewPredicateCandidate, error) {
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
	return scanOneTeamPredicateCandidate(rows)
}

func loadTeamPredicateCandidateByExactKey(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	key string,
) (*SemanticReviewPredicateCandidate, error) {
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
	return scanOneTeamPredicateCandidate(rows)
}

func ensureTeamPredicateCandidateKinds(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureSemanticPredicateCandidateInput,
	candidate SemanticReviewPredicateCandidate,
) (*SemanticReviewPredicateCandidate, error) {
	subjectKinds := unionStringSet(candidate.AllowedSubjectKinds, []string{input.SubjectKind})
	objectKinds := unionStringSet(candidate.AllowedObjectKinds, []string{input.ObjectKind})
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
	data, err := marshalJSON(metadata)
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
	`, input.TeamID, candidate.PredicateKey, candidate.Version+1, postgresStringArray(subjectKinds),
		postgresStringArray(objectKinds), candidate.RelationshipKind, candidate.CurrentCardinality,
		input.Origin, string(data)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, err := scanOneTeamPredicateCandidate(rows)
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func insertTeamPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	input EnsureSemanticPredicateCandidateInput,
	key string,
	collision bool,
) (*SemanticReviewPredicateCandidate, error) {
	metadata := map[string]any{
		"source":             "generated_predicate",
		"original_predicate": input.Predicate,
	}
	for name, value := range input.Metadata {
		metadata[name] = value
	}
	data, err := marshalJSON(metadata)
	if err != nil {
		return nil, err
	}
	aliases := []string{}
	if !collision {
		aliases = unionStringSet(aliases, []string{input.Predicate})
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
	`, input.TeamID, key, postgresStringArray(aliases), postgresStringArray([]string{input.SubjectKind}),
		postgresStringArray([]string{input.ObjectKind}), input.RelationshipKind, input.Origin,
		string(data)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, err := scanOneTeamPredicateCandidate(rows)
	if errors.Is(err, sql.ErrNoRows) {
		return loadTeamPredicateCandidateByExactKey(ctx, tx, input.TeamID, key)
	}
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func scanOneTeamPredicateCandidate(rows *sql.Rows) (*SemanticReviewPredicateCandidate, error) {
	defer rows.Close()
	candidates, err := scanSemanticReviewPredicateCandidates(rows)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, sql.ErrNoRows
	}
	return &candidates[0], rows.Err()
}

func canonicalGeneratedPredicateKey(value string) string {
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
		return "predicate_" + shortPredicateHash(value, 12)
	}
	return string(out)
}

func collisionGeneratedPredicateKey(base string, relationshipKind string, original string) string {
	suffix := "__" + strings.TrimSpace(relationshipKind) + "_" + shortPredicateHash(original+":"+relationshipKind, 8)
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

func shortPredicateHash(value string, n int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if n <= 0 || n > len(encoded) {
		return encoded
	}
	return encoded[:n]
}

func unionStringSet(left []string, right []string) []string {
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

func normalizeReviewOptionLimit(limit int) int {
	if limit <= 0 {
		return semanticReviewDefaultOptionLimit
	}
	if limit > semanticReviewMaxOptionLimit {
		return semanticReviewMaxOptionLimit
	}
	return limit
}

func loadSemanticReviewEntityCandidateByID(
	ctx context.Context,
	tx *gorm.DB,
	input SemanticReviewEntityCandidateInput,
) (*SemanticReviewEntityCandidate, error) {
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
	candidates, err := scanSemanticReviewEntityCandidates(rows)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, sql.ErrNoRows
	}
	return &candidates[0], rows.Err()
}

func loadSemanticReviewEntityCandidatesByName(
	ctx context.Context,
	tx *gorm.DB,
	input SemanticReviewEntityCandidateInput,
	limit int,
) ([]SemanticReviewEntityCandidate, error) {
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
	`, input.TeamID, normalizeName(input.Name), input.EntityKind, input.EntityKind, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, err := scanSemanticReviewEntityCandidates(rows)
	if err != nil {
		return nil, err
	}
	return candidates, rows.Err()
}

func scanSemanticReviewEntityCandidates(rows *sql.Rows) ([]SemanticReviewEntityCandidate, error) {
	out := []SemanticReviewEntityCandidate{}
	for rows.Next() {
		var candidate SemanticReviewEntityCandidate
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

func scanSemanticReviewPredicateCandidates(rows *sql.Rows) ([]SemanticReviewPredicateCandidate, error) {
	out := []SemanticReviewPredicateCandidate{}
	for rows.Next() {
		var candidate SemanticReviewPredicateCandidate
		var subjectKinds postgrescompat.StringArray
		var objectKinds postgrescompat.StringArray
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
