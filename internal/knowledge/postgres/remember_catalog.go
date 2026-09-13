package postgres

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
	submissionAssessmentMaxEntityTargets    = 400
	submissionAssessmentMaxCandidateSet     = 20
	submissionAssessmentMaxKnownEvidenceIDs = 4000
	semanticReviewDefaultCandidateLimit     = 5
	semanticReviewMaxCandidateLimit         = 20
	semanticReviewDefaultOptionLimit        = 100
	semanticReviewMaxOptionLimit            = 100
	semanticReviewMaxResolutionLimit        = semanticReviewMaxOptionLimit + 1
	semanticReviewMaxResolutionInputs       = 200
)

func (r *Store) ListSubmissionAssessmentEntityCatalog(
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
			normalizedSurface := normalizeName(target.Surface)
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
				                 AND active_name.space_id = rec.space_id
				                 AND `+activeSemanticSpaceGenerationSQL("active_name")+`
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
				     AND name.space_id = rec.space_id
				     AND `+activeSemanticSpaceGenerationSQL("name")+`
				     AND name.valid_to IS NULL
				     AND name.name_kind IN ('canonical', 'alias')
				    LEFT JOIN entity_names AS canonical
				      ON canonical.team_id = rec.team_id
				     AND canonical.entity_id = rec.entity_id
				     AND canonical.space_id = rec.space_id
				     AND `+activeSemanticSpaceGenerationSQL("canonical")+`
				     AND canonical.name_kind = 'canonical'
				     AND canonical.valid_to IS NULL
				    WHERE rec.team_id = ?::uuid
				      AND rec.status = 'active'
				      AND rec.space_id = COALESCE(NULLIF(?, '')::uuid, dense_mem_team_shared_space(?::uuid))
				      AND `+activeSemanticSpaceGenerationSQL("rec")+`
				      AND (? = '' OR rec.entity_kind = ?)
				      AND (
				          rec.entity_id = NULLIF(?, '')::uuid
				          OR (
				              name.normalized_name = ?
				              AND NOT EXISTS (
				                  SELECT 1
				                  FROM entity_records AS exact
				                  WHERE exact.team_id = rec.team_id
				                    AND exact.entity_id = NULLIF(?, '')::uuid
				                    AND exact.status = 'active'
				                    AND exact.space_id = rec.space_id
				                    AND `+activeSemanticSpaceGenerationSQL("exact")+`
				              )
				          )
				      )
				    ORDER BY rec.entity_id, known_rank, name_rank, name.created_at DESC
				)
				SELECT team_id, entity_id, entity_kind, canonical_name, active_names, identity_context, status
				FROM candidates
				ORDER BY known_rank, name_rank, entity_id
				LIMIT ?
			`, target.KnownEntityID, normalizedSurface, input.TeamID, input.SpaceID, input.TeamID,
				target.EntityKind, target.EntityKind, target.KnownEntityID, normalizedSurface, target.KnownEntityID, input.CandidateLimit+1).Rows()
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
	input.SpaceID = strings.TrimSpace(input.SpaceID)
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
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
	}
	if len(input.Entities) > submissionAssessmentMaxEntityTargets {
		return fmt.Errorf("entities must contain at most %d entries", submissionAssessmentMaxEntityTargets)
	}
	if input.CandidateLimit < 1 || input.CandidateLimit > submissionAssessmentMaxCandidateSet {
		return fmt.Errorf("candidate_limit must be between 1 and %d", submissionAssessmentMaxCandidateSet)
	}
	for _, target := range input.Entities {
		if target.Ref == "" || len([]rune(target.Ref)) > 128 {
			return errors.New("entity ref is required and must be bounded")
		}
		if target.Surface != "" && len([]rune(target.Surface)) > 1000 {
			return errors.New("entity surface must be bounded")
		}
		if target.Surface == "" && target.KnownEntityID == "" {
			return errors.New("entity surface or known_entity_id is required")
		}
		if target.EntityKind != "" && !contains(domain.EntityKinds(), target.EntityKind) {
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

func (r *Store) ResolveSemanticReviewPredicateCandidates(
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
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
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
		`, pq.Array(input.Predicates), pq.Array(normalizedPredicates), input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var resolution SemanticReviewPredicateResolution
			var aliases pq.StringArray
			var subjectKinds pq.StringArray
			var objectKinds pq.StringArray
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

func (r *Store) ListSemanticAssessmentPredicateOptions(
	ctx context.Context,
	input SemanticAssessmentPredicateOptionsInput,
) ([]SemanticReviewPredicateCandidate, error) {
	input = normalizeSemanticAssessmentPredicateOptionsInput(input)
	if err := validateSemanticAssessmentPredicateOptionsInput(input); err != nil {
		return nil, err
	}
	out := []SemanticReviewPredicateCandidate{}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH latest AS (
			    SELECT DISTINCT ON (predicate_key)
			           predicate_key, version, aliases, allowed_subject_kinds,
			           allowed_object_kinds, relationship_kind, current_cardinality,
			           lifecycle_state, origin, created_at
			    FROM team_predicate_definitions
			    WHERE team_id = ?::uuid
			    ORDER BY predicate_key, version DESC
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

func normalizeReviewOptionLimit(limit int) int {
	if limit <= 0 {
		return semanticReviewDefaultOptionLimit
	}
	if limit > semanticReviewMaxOptionLimit {
		return semanticReviewMaxOptionLimit
	}
	return limit
}

func (r *Store) ListSubmissionAssessmentKnownEvidence(
	ctx context.Context,
	input SubmissionAssessmentKnownEvidenceInput,
) (SubmissionAssessmentKnownEvidenceResult, error) {
	input = normalizeSubmissionAssessmentKnownEvidenceInput(input)
	if err := validateSubmissionAssessmentKnownEvidenceInput(input); err != nil {
		return SubmissionAssessmentKnownEvidenceResult{}, err
	}
	result := SubmissionAssessmentKnownEvidenceResult{Evidence: []SubmissionAssessmentKnownEvidence{}}
	if len(input.EvidenceIDs) == 0 {
		return result, nil
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT fragment.team_id::text,
			       fragment.fragment_id::text,
			       fragment.ingest_id::text,
			       fragment.owner_profile_id::text,
			       fragment.content,
			       fragment.content_hash,
			       fragment.authority,
			       COALESCE(fragment.source_id::text, ''),
			       COALESCE(fragment.source_revision_id::text, ''),
			       COALESCE(source.current_revision_id::text, ''),
			       fragment.space_id::text,
			       fragment.space_generation
			FROM evidence_fragments AS fragment
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = fragment.team_id
			 AND ingest.ingest_id = fragment.ingest_id
			 AND ingest.owner_profile_id = fragment.owner_profile_id
			JOIN memory_spaces AS space
			  ON space.team_id = fragment.team_id
			 AND space.id = fragment.space_id
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			 AND source.owner_profile_id = fragment.owner_profile_id
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = fragment.team_id
			 AND quarantine.fragment_id = fragment.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_lifecycle_events AS lifecycle
			  ON lifecycle.team_id = fragment.team_id
			 AND lifecycle.target_fragment_id = fragment.fragment_id
			WHERE fragment.team_id = ?::uuid
			  AND fragment.space_id = COALESCE(NULLIF(?, '')::uuid, dense_mem_team_shared_space(?::uuid))
			  AND fragment.fragment_id = ANY(?::uuid[])
			  AND ingest.status = 'completed'
			  AND space.lifecycle_state = 'active'
			  AND (space.kind = 'team_shared' OR dense_mem_space_allowed(space.id))
			  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
			  AND quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
			  AND NOT (
			      ingest.source_summary = 'overdue conflict deletion-only derivation'
			      AND ingest.metadata ->> 'conflict_resolution_deletion_only' = 'true'
			  )
			ORDER BY fragment.fragment_id
		`, input.TeamID, input.SpaceID, input.TeamID, pq.Array(input.EvidenceIDs)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SubmissionAssessmentKnownEvidence
			if err := rows.Scan(
				&item.TeamID,
				&item.EvidenceID,
				&item.IngestID,
				&item.OwnerProfileID,
				&item.Content,
				&item.ContentHash,
				&item.Authority,
				&item.SourceID,
				&item.SourceRevisionID,
				&item.CurrentSourceRevisionID,
				&item.SpaceID,
				&item.SpaceGeneration,
			); err != nil {
				return err
			}
			item.FragmentID = item.EvidenceID
			result.Evidence = append(result.Evidence, item)
		}
		return rows.Err()
	})
	if err != nil {
		return SubmissionAssessmentKnownEvidenceResult{}, fmt.Errorf("semantic: list submission assessment known evidence: %w", err)
	}
	return result, nil
}

func normalizeSubmissionAssessmentKnownEvidenceInput(input SubmissionAssessmentKnownEvidenceInput) SubmissionAssessmentKnownEvidenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	seen := make(map[string]struct{}, len(input.EvidenceIDs))
	ids := make([]string, 0, len(input.EvidenceIDs))
	for _, evidenceID := range input.EvidenceIDs {
		evidenceID = strings.TrimSpace(evidenceID)
		if evidenceID == "" {
			continue
		}
		if parsed, err := uuid.Parse(evidenceID); err == nil {
			evidenceID = parsed.String()
		}
		if _, exists := seen[evidenceID]; exists {
			continue
		}
		seen[evidenceID] = struct{}{}
		ids = append(ids, evidenceID)
	}
	input.EvidenceIDs = ids
	return input
}

func validateSubmissionAssessmentKnownEvidenceInput(input SubmissionAssessmentKnownEvidenceInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
	}
	if len(input.EvidenceIDs) > submissionAssessmentMaxKnownEvidenceIDs {
		return fmt.Errorf("evidence_ids must contain at most %d entries", submissionAssessmentMaxKnownEvidenceIDs)
	}
	for _, evidenceID := range input.EvidenceIDs {
		if _, err := uuid.Parse(evidenceID); err != nil {
			return fmt.Errorf("evidence_id is invalid: %w", err)
		}
	}
	return nil
}
