package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const semanticAssessmentMaxKnownEntityIDs = 100

// ListSemanticAssessmentEntityMatches returns only current names whose text is
// present in this one evidence item. It never performs vector retrieval or
// emits a team-wide entity catalogue; rune-token boundary validation remains in
// the application service because PostgreSQL character positions are not the
// contract's rune offsets.
func (r *SemanticRepositoryImpl) ListSemanticAssessmentEntityMatches(
	ctx context.Context,
	input SemanticAssessmentEntityMatchInput,
) (SemanticAssessmentEntityMatchResult, error) {
	input = normalizeSemanticAssessmentEntityMatchInput(input)
	if err := validateSemanticAssessmentEntityMatchInput(input); err != nil {
		return SemanticAssessmentEntityMatchResult{}, err
	}
	result := SemanticAssessmentEntityMatchResult{Matches: []SemanticAssessmentEntityMatch{}}
	if input.EvidenceText == "" {
		return result, nil
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT DISTINCT ON (name.entity_id, lower(name.display_name))
			       rec.team_id::text, rec.entity_id::text, rec.entity_kind,
			       COALESCE(canonical.display_name, name.display_name),
			       rec.identity_context, rec.status, name.display_name
			FROM entity_names AS name
			JOIN entity_records AS rec
			  ON rec.team_id = name.team_id
			 AND rec.entity_id = name.entity_id
			 AND `+activeSemanticSpaceGenerationSQL("rec")+`
			LEFT JOIN entity_names AS canonical
			  ON canonical.team_id = rec.team_id
			 AND canonical.entity_id = rec.entity_id
			 AND `+activeSemanticSpaceGenerationSQL("canonical")+`
			 AND canonical.name_kind = 'canonical'
			 AND canonical.valid_to IS NULL
			WHERE name.team_id = ?::uuid
			  AND `+activeSemanticSpaceGenerationSQL("name")+`
			  AND name.valid_to IS NULL
			  AND name.name_kind IN ('canonical', 'alias')
			  AND rec.status = 'active'
			  AND btrim(name.display_name) <> ''
			  AND strpos(lower(?), lower(name.display_name)) > 0
			ORDER BY name.entity_id,
			         lower(name.display_name),
			         CASE WHEN name.name_kind = 'canonical' THEN 0 ELSE 1 END,
			         name.created_at DESC
			LIMIT ?
		`, input.TeamID, input.EvidenceText, input.Limit+1).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var match SemanticAssessmentEntityMatch
			var contextRaw []byte
			if err := rows.Scan(
				&match.Candidate.TeamID,
				&match.Candidate.EntityID,
				&match.Candidate.EntityKind,
				&match.Candidate.CanonicalName,
				&contextRaw,
				&match.Candidate.Status,
				&match.MatchedName,
			); err != nil {
				return err
			}
			if len(contextRaw) > 0 {
				if err := json.Unmarshal(contextRaw, &match.Candidate.IdentityContext); err != nil {
					return err
				}
			}
			if len(result.Matches) == input.Limit {
				result.Truncated = true
				continue
			}
			result.Matches = append(result.Matches, match)
		}
		return rows.Err()
	})
	if err != nil {
		return SemanticAssessmentEntityMatchResult{}, fmt.Errorf("semantic: list assessment entity matches: %w", err)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) ListSemanticAssessmentKnownEntities(
	ctx context.Context,
	input SemanticAssessmentKnownEntityInput,
) ([]SemanticReviewEntityCandidate, error) {
	input = normalizeSemanticAssessmentKnownEntityInput(input)
	if err := validateSemanticAssessmentKnownEntityInput(input); err != nil {
		return nil, err
	}
	if len(input.EntityIDs) == 0 {
		return []SemanticReviewEntityCandidate{}, nil
	}
	out := []SemanticReviewEntityCandidate{}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT rec.team_id::text, rec.entity_id::text, rec.entity_kind,
			       COALESCE(canonical.display_name, ''), rec.identity_context, rec.status
			FROM entity_records AS rec
			LEFT JOIN entity_names AS canonical
			  ON canonical.team_id = rec.team_id
			 AND canonical.entity_id = rec.entity_id
			 AND `+activeSemanticSpaceGenerationSQL("canonical")+`
			 AND canonical.name_kind = 'canonical'
			 AND canonical.valid_to IS NULL
			WHERE rec.team_id = ?::uuid
			  AND `+activeSemanticSpaceGenerationSQL("rec")+`
			  AND rec.entity_id = ANY(?::uuid[])
			  AND rec.status = 'active'
			ORDER BY rec.entity_id
		`, input.TeamID, pq.Array(input.EntityIDs)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		candidates, err := scanSemanticReviewEntityCandidates(rows)
		if err != nil {
			return err
		}
		out = candidates
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: list assessment known entities: %w", err)
	}
	return out, nil
}

func normalizeSemanticAssessmentKnownEntityInput(
	input SemanticAssessmentKnownEntityInput,
) SemanticAssessmentKnownEntityInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	seen := make(map[string]struct{}, len(input.EntityIDs))
	ids := make([]string, 0, len(input.EntityIDs))
	for _, entityID := range input.EntityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if _, exists := seen[entityID]; exists {
			continue
		}
		seen[entityID] = struct{}{}
		ids = append(ids, entityID)
	}
	input.EntityIDs = ids
	return input
}

func validateSemanticAssessmentKnownEntityInput(input SemanticAssessmentKnownEntityInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.EntityIDs) > semanticAssessmentMaxKnownEntityIDs {
		return fmt.Errorf("entity_ids must contain at most %d entries", semanticAssessmentMaxKnownEntityIDs)
	}
	for _, entityID := range input.EntityIDs {
		if _, err := uuid.Parse(entityID); err != nil {
			return fmt.Errorf("entity_id is invalid: %w", err)
		}
	}
	return nil
}

// ListSemanticAssessmentPredicateOptions returns active registered definitions
// only. Client-proposed key/alias matches sort before evidence wording and
// lexical relevance; the assessor may select a supplied definition or abstain,
// never register one.
func (r *SemanticRepositoryImpl) ListSemanticAssessmentPredicateOptions(
	ctx context.Context,
	input SemanticAssessmentPredicateOptionsInput,
) ([]SemanticReviewPredicateCandidate, error) {
	input = normalizeSemanticAssessmentPredicateOptionsInput(input)
	if err := validateSemanticAssessmentPredicateOptionsInput(input); err != nil {
		return nil, err
	}
	out := []SemanticReviewPredicateCandidate{}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH latest_team_definitions AS (
			    SELECT DISTINCT ON (predicate_key)
			           predicate_key, version, aliases, allowed_subject_kinds,
			           allowed_object_kinds, relationship_kind, current_cardinality,
			           lifecycle_state, origin, created_at
			    FROM team_predicate_definitions
			    WHERE team_id = ?::uuid
			    ORDER BY predicate_key, version DESC
			), latest_builtin_definitions AS (
			    SELECT DISTINCT ON (predicate_key)
			           predicate_key, version, aliases, allowed_subject_kinds,
			           allowed_object_kinds, relationship_kind, current_cardinality,
			           lifecycle_state, created_at
			    FROM predicate_definitions
			    ORDER BY predicate_key, version DESC
			), latest AS (
			    SELECT * FROM latest_team_definitions
			    UNION ALL
			    SELECT builtin.predicate_key, builtin.version, builtin.aliases,
			           builtin.allowed_subject_kinds, builtin.allowed_object_kinds,
			           builtin.relationship_kind, builtin.current_cardinality,
			           builtin.lifecycle_state, 'built_in'::text, builtin.created_at
			    FROM latest_builtin_definitions AS builtin
			    WHERE NOT EXISTS (
			        SELECT 1
			        FROM latest_team_definitions AS team_definition
			        WHERE team_definition.predicate_key = builtin.predicate_key
			    )
			), proposed_terms AS (
			    SELECT DISTINCT btrim(regexp_replace(lower(replace(term, '_', ' ')), '[[:space:]]+', ' ', 'g')) AS term
			    FROM unnest(?::text[]) AS proposed(term)
			    WHERE btrim(term) <> ''
			), evidence_terms AS (
			    SELECT DISTINCT evidence_term.term
			    FROM unnest(tsvector_to_array(to_tsvector('english', ?)))
			         AS evidence_term(term)
			), ranked AS (
			    SELECT latest.*,
			           CASE WHEN EXISTS (
			                         SELECT 1
			                         FROM proposed_terms
			                         WHERE proposed_terms.term = btrim(regexp_replace(lower(replace(latest.predicate_key, '_', ' ')), '[[:space:]]+', ' ', 'g'))
			                            OR EXISTS (
			                                SELECT 1
			                                FROM unnest(latest.aliases) AS alias(value)
			                                WHERE proposed_terms.term = btrim(regexp_replace(lower(replace(alias.value, '_', ' ')), '[[:space:]]+', ' ', 'g'))
			                            )
			                    ) THEN 0 ELSE 1 END AS proposed_rank,
			           CASE WHEN strpos(lower(?), lower(replace(latest.predicate_key, '_', ' '))) > 0
			                     OR EXISTS (
			                         SELECT 1 FROM unnest(latest.aliases) AS alias(value)
			                         WHERE strpos(lower(?), lower(replace(alias.value, '_', ' '))) > 0
			                     )
			                THEN 0 ELSE 1 END AS exact_rank,
			           (
			               SELECT count(*)
			               FROM unnest(tsvector_to_array(to_tsvector(
			                   'english',
			                   replace(latest.predicate_key, '_', ' ') || ' ' ||
			                   array_to_string(latest.aliases, ' ')
			               ))) AS predicate_term(term)
			               JOIN evidence_terms ON evidence_terms.term = predicate_term.term
			           ) AS relevance
			    FROM latest
			    WHERE lifecycle_state = 'active'
			)
			SELECT predicate_key, version, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality,
			       lifecycle_state
			FROM ranked
			ORDER BY proposed_rank ASC,
			         exact_rank ASC,
			         relevance DESC,
			         CASE WHEN origin = 'built_in' THEN 0 ELSE 1 END,
			         created_at DESC,
			         predicate_key ASC
			LIMIT ?
		`, input.TeamID, pq.Array(input.ProposedKeys), input.QueryText, input.QueryText, input.QueryText, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var candidate SemanticReviewPredicateCandidate
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
			candidate.Aliases = []string(aliases)
			candidate.AllowedSubjectKinds = []string(subjectKinds)
			candidate.AllowedObjectKinds = []string(objectKinds)
			out = append(out, candidate)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: list assessment predicate options: %w", err)
	}
	return out, nil
}
