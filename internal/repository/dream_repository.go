package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var (
	ErrDreamCycleAlreadyClaimed     = errors.New("dream cycle already claimed")
	ErrDreamHypothesisNotFound      = errors.New("dream hypothesis not found")
	ErrDreamSourceStale             = errors.New("dream source is stale")
	ErrDreamExactRelationshipExists = errors.New("dream exact relationship already exists")
	ErrDreamExactHypothesisExists   = errors.New("dream exact hypothesis already exists")
	ErrDreamCycleLeaseLost          = errors.New("dream cycle lease is no longer current")
)

const hypothesisSourceIneligiblePredicateSQL = `EXISTS (
	SELECT 1
	FROM jsonb_each_text(hypotheses.source_versions) AS source(source_id, source_version)
		LEFT JOIN relationship_records r
		  ON r.team_id = hypotheses.team_id
		 AND r.relationship_id = CASE
	     WHEN source.source_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
	     THEN source.source_id::uuid
		     ELSE NULL
		 END
		 AND r.space_id = hypotheses.space_id
		 AND r.space_generation = hypotheses.space_generation
	WHERE r.relationship_id IS NULL
	   OR r.version::text <> source.source_version
	   OR NOT (
	     (r.status = 'active' AND r.support_count > 0)
	     OR (
	       r.status = 'pending_evidence'
	       AND EXISTS (
	         SELECT 1
			FROM relationship_observations o
			JOIN verification_events v
			  ON v.team_id = o.team_id
			 AND v.observation_id = o.observation_id
			 AND v.space_id = o.space_id
			 AND v.space_generation = o.space_generation
			WHERE o.team_id = r.team_id
			  AND o.space_id = r.space_id
			  AND o.space_generation = r.space_generation
	           AND o.relationship_id = r.relationship_id
	           AND v.evidence_verdict = 'insufficient'
	       )
	     )
	   )
	   OR EXISTS (
	     SELECT 1
	     FROM relationship_cross_references cr
			WHERE cr.team_id = r.team_id
		       AND cr.space_id = r.space_id
		       AND cr.space_generation = r.space_generation
		       AND cr.target_relationship_id = r.relationship_id
	       AND cr.kind = 'challenges'
	   )
) OR ` + hypothesisExactDerivationIneligiblePredicateSQL

const hypothesisExactDerivationIneligiblePredicateSQL = `(hypotheses.generator_kind <> 'provider'
	OR NOT EXISTS (
		SELECT 1
		FROM hypothesis_derivation_sources derivation
		WHERE derivation.team_id = hypotheses.team_id
		  AND derivation.space_id = hypotheses.space_id
		  AND derivation.space_generation = hypotheses.space_generation
		  AND derivation.hypothesis_id = hypotheses.hypothesis_id
		  AND derivation.premise_position = 1
	)
	OR NOT EXISTS (
		SELECT 1
		FROM hypothesis_derivation_sources derivation
		WHERE derivation.team_id = hypotheses.team_id
		  AND derivation.space_id = hypotheses.space_id
		  AND derivation.space_generation = hypotheses.space_generation
		  AND derivation.hypothesis_id = hypotheses.hypothesis_id
		  AND derivation.premise_position = 2
	)
	OR EXISTS (
		SELECT 1
		FROM hypothesis_derivation_sources derivation
		LEFT JOIN relationship_records relationship
		  ON relationship.team_id = derivation.team_id
		 AND relationship.relationship_id = derivation.relationship_id
		 AND relationship.space_id = derivation.space_id
		 AND relationship.space_generation = derivation.space_generation
		WHERE derivation.team_id = hypotheses.team_id
		  AND derivation.space_id = hypotheses.space_id
		  AND derivation.space_generation = hypotheses.space_generation
		  AND derivation.hypothesis_id = hypotheses.hypothesis_id
		  AND NOT COALESCE(
			relationship.relationship_id IS NOT NULL
			AND relationship.identity_alias_of_relationship_id IS NULL
			AND relationship.version = derivation.relationship_version
			AND EXISTS (
				SELECT 1
					FROM entity_records subject_entity
					WHERE subject_entity.team_id = relationship.team_id
					  AND subject_entity.entity_id = relationship.subject_entity_id
					  AND subject_entity.space_id = relationship.space_id
					  AND subject_entity.space_generation = relationship.space_generation
				  AND subject_entity.status = 'active'
			)
			AND (
				relationship.object_entity_id IS NULL
				OR EXISTS (
					SELECT 1
					FROM entity_records object_entity
					WHERE object_entity.team_id = relationship.team_id
					  AND object_entity.entity_id = relationship.object_entity_id
					  AND object_entity.space_id = relationship.space_id
					  AND object_entity.space_generation = relationship.space_generation
					  AND object_entity.status = 'active'
				)
			)
			AND NOT EXISTS (
				SELECT 1
				FROM relationship_cross_references cross_reference
					WHERE cross_reference.team_id = relationship.team_id
					  AND cross_reference.space_id = relationship.space_id
					  AND cross_reference.space_generation = relationship.space_generation
				  AND cross_reference.target_relationship_id = relationship.relationship_id
				  AND cross_reference.kind = 'challenges'
			)
			AND NOT EXISTS (
				SELECT 1
				FROM review_tasks review
					WHERE review.team_id = relationship.team_id
					  AND review.space_id = relationship.space_id
					  AND review.space_generation = relationship.space_generation
				  AND review.relationship_id = relationship.relationship_id
				  AND review.status IN ('open', 'acknowledged')
			)
			AND (
				(
					relationship.status = 'active'
					AND derivation.support_id IS NOT NULL
					AND EXISTS (
						SELECT 1
						FROM relationship_evidence_supports support
						JOIN evidence_fragments fragment
						  ON fragment.team_id = support.team_id
						 AND fragment.fragment_id = support.fragment_id
						 AND fragment.space_id = support.space_id
						 AND fragment.space_generation = support.space_generation
			LEFT JOIN evidence_sources source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			 AND source.space_id = support.space_id
			 AND source.space_generation = support.space_generation
						WHERE support.team_id = derivation.team_id
						  AND support.space_id = derivation.space_id
						  AND support.space_generation = derivation.space_generation
						  AND support.support_id = derivation.support_id
						  AND support.relationship_id = derivation.relationship_id
						  AND support.fragment_id = derivation.fragment_id
						  AND support.source_id IS NOT DISTINCT FROM derivation.source_id
						  AND support.source_revision_id IS NOT DISTINCT FROM derivation.source_revision_id
						  AND support.source_group_key = derivation.source_group_key
						  AND support.span_start = derivation.span_start
						  AND support.span_end = derivation.span_end
						  AND support.authority = derivation.authority
						  AND substring(fragment.content FROM support.span_start + 1 FOR support.span_end - support.span_start) = derivation.quote
						  AND COALESCE((
							SELECT decision.decision
								FROM relationship_support_decision_events decision
								WHERE decision.team_id = support.team_id
								  AND decision.space_id = support.space_id
								  AND decision.space_generation = support.space_generation
							  AND decision.support_id = support.support_id
							ORDER BY decision.created_at DESC, decision.support_decision_id DESC
							LIMIT 1
						), '') IN ('grant', 'reinstate')
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_quarantines quarantine
								WHERE quarantine.team_id = support.team_id
								  AND quarantine.space_id = support.space_id
								  AND quarantine.space_generation = support.space_generation
							  AND quarantine.fragment_id = support.fragment_id
							  AND quarantine.status = 'active'
						)
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_lifecycle_events lifecycle
								WHERE lifecycle.team_id = support.team_id
								  AND lifecycle.space_id = support.space_id
								  AND lifecycle.space_generation = support.space_generation
							  AND lifecycle.target_fragment_id = support.fragment_id
						)
						  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
					)
				)
				OR (
					relationship.status = 'pending_evidence'
					AND derivation.observation_id IS NOT NULL
					AND EXISTS (
						SELECT 1
						FROM relationship_observations observation
						JOIN verification_events verification
						  ON verification.team_id = observation.team_id
						 AND verification.observation_id = observation.observation_id
						 AND verification.space_id = observation.space_id
						 AND verification.space_generation = observation.space_generation
						JOIN placement_assessments assessment
						  ON assessment.team_id = verification.team_id
						 AND assessment.assessment_id = verification.assessment_id
						 AND assessment.space_id = verification.space_id
						 AND assessment.space_generation = verification.space_generation
						JOIN review_tasks review
						  ON review.team_id = verification.team_id
						 AND review.assessment_id = verification.assessment_id
						 AND review.space_id = verification.space_id
						 AND review.space_generation = verification.space_generation
						JOIN LATERAL jsonb_array_elements(observation.evidence) evidence(value) ON true
						JOIN evidence_fragments fragment
						  ON fragment.team_id = observation.team_id
						 AND evidence.value->>'fragment_id' = fragment.fragment_id::text
						 AND fragment.space_id = observation.space_id
						 AND fragment.space_generation = observation.space_generation
			LEFT JOIN evidence_sources source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			 AND source.space_id = fragment.space_id
			 AND source.space_generation = fragment.space_generation
						WHERE observation.team_id = derivation.team_id
						  AND observation.space_id = derivation.space_id
						  AND observation.space_generation = derivation.space_generation
						  AND observation.observation_id = derivation.observation_id
						  AND observation.relationship_id = derivation.relationship_id
						  AND verification.evidence_verdict IN ('insufficient', 'entailed')
						  AND verification.gate_result = 'below_write_threshold'
						  AND review.status = 'expired'
						  AND fragment.fragment_id = derivation.fragment_id
						  AND fragment.source_id IS NOT DISTINCT FROM derivation.source_id
						  AND fragment.source_revision_id IS NOT DISTINCT FROM derivation.source_revision_id
						  AND COALESCE(NULLIF(fragment.source_ref, ''), 'pending_observation') = derivation.source_group_key
						  AND fragment.authority = derivation.authority
						  AND evidence.value->>'start' = derivation.span_start::text
						  AND evidence.value->>'end' = derivation.span_end::text
						  AND substring(fragment.content FROM derivation.span_start + 1 FOR derivation.span_end - derivation.span_start) = derivation.quote
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_quarantines quarantine
								WHERE quarantine.team_id = fragment.team_id
								  AND quarantine.space_id = fragment.space_id
								  AND quarantine.space_generation = fragment.space_generation
							  AND quarantine.fragment_id = fragment.fragment_id
							  AND quarantine.status = 'active'
						)
						  AND NOT EXISTS (
							SELECT 1
								FROM evidence_lifecycle_events lifecycle
								WHERE lifecycle.team_id = fragment.team_id
								  AND lifecycle.space_id = fragment.space_id
								  AND lifecycle.space_generation = fragment.space_generation
							  AND lifecycle.target_fragment_id = fragment.fragment_id
						)
						  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
					)
				)
			), false)
	)
)`

func insertHypothesisFeedbackEvent(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	hypothesisID string,
	actorProfileID string,
	decision string,
	feedback string,
	submittedIngestID string,
) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO hypothesis_feedback_events (
		    team_id, space_id, space_generation, hypothesis_id, actor_profile_id, decision, feedback,
		    submitted_ingest_id
		)
		SELECT ?::uuid, hypothesis.space_id, hypothesis.space_generation, ?::uuid, ?::uuid, ?, COALESCE(NULLIF(?, ''), ''),
		       NULLIF(?, '')::uuid
		FROM hypotheses AS hypothesis
		WHERE hypothesis.team_id = ?::uuid
		  AND hypothesis.space_id = dense_mem_team_shared_space(hypothesis.team_id)
		  AND hypothesis.space_generation = dense_mem_team_shared_generation(hypothesis.team_id)
		  AND hypothesis.hypothesis_id = ?::uuid
	`, teamID, hypothesisID, actorProfileID, decision, feedback, submittedIngestID, teamID, hypothesisID).Error
}

func (r *SemanticRepositoryImpl) ListDreamInputs(ctx context.Context, input DreamInputListInput) ([]DreamInput, error) {
	input = normalizeDreamInputListInput(input)
	if err := validateDreamInputListInput(input); err != nil {
		return nil, err
	}
	var inputs []DreamInput
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH latest_support_decision AS (
				SELECT DISTINCT ON (support_id)
				       support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				  AND space_id = dense_mem_team_shared_space(team_id)
				  AND space_generation = dense_mem_team_shared_generation(team_id)
				ORDER BY support_id, created_at DESC, support_decision_id DESC
			), effective_support AS (
				SELECT support.relationship_id, count(*)::int AS support_count
				FROM relationship_evidence_supports support
				JOIN latest_support_decision decision
				  ON decision.support_id = support.support_id
				LEFT JOIN evidence_quarantines quarantine
				  ON quarantine.team_id = support.team_id
				 AND quarantine.fragment_id = support.fragment_id
				 AND quarantine.status = 'active'
				LEFT JOIN evidence_lifecycle_events lifecycle
				  ON lifecycle.team_id = support.team_id
				 AND lifecycle.target_fragment_id = support.fragment_id
				LEFT JOIN evidence_sources source
				  ON source.team_id = support.team_id
				 AND source.source_id = support.source_id
				WHERE support.team_id = ?::uuid
				  AND support.space_id = dense_mem_team_shared_space(support.team_id)
				  AND support.space_generation = dense_mem_team_shared_generation(support.team_id)
				  AND decision.decision IN ('grant', 'reinstate')
				  AND quarantine.quarantine_id IS NULL
				  AND lifecycle.lifecycle_event_id IS NULL
				  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
				GROUP BY support.relationship_id
			), eligible_pending AS (
				SELECT DISTINCT ON (observation.relationship_id)
				       observation.relationship_id
				FROM relationship_observations observation
				JOIN verification_events verification
				  ON verification.team_id = observation.team_id
				 AND verification.observation_id = observation.observation_id
				JOIN placement_assessments assessment
				  ON assessment.team_id = verification.team_id
				 AND assessment.assessment_id = verification.assessment_id
				JOIN review_tasks review
				  ON review.team_id = verification.team_id
				 AND review.assessment_id = verification.assessment_id
				WHERE observation.team_id = ?::uuid
				  AND observation.space_id = dense_mem_team_shared_space(observation.team_id)
				  AND observation.space_generation = dense_mem_team_shared_generation(observation.team_id)
				  AND observation.relationship_id IS NOT NULL
				  AND verification.evidence_verdict IN ('insufficient', 'entailed')
				  AND verification.gate_result = 'below_write_threshold'
				  AND review.status = 'expired'
				ORDER BY observation.relationship_id, verification.created_at DESC, verification.verification_event_id DESC
			)
			SELECT r.relationship_id::text,
			       r.owner_profile_id::text,
			       r.version,
			       r.status,
			       r.subject_entity_id::text,
			       COALESCE(subject_name.display_name, '') AS subject_name,
			       subject_entity.entity_kind AS subject_kind,
			       r.predicate_key,
			       r.predicate_version,
			       COALESCE(r.object_entity_id::text, '') AS object_entity_id,
			       COALESCE(r.object_value_id::text, '') AS object_value_id,
			       COALESCE(object_name.display_name, value.display, '') AS object_name,
			       COALESCE(object_entity.entity_kind, value.value_type, '') AS object_kind,
			       r.relationship_kind,
			       r.current_cardinality
			FROM relationship_records r
			JOIN entity_records subject_entity
			  ON subject_entity.team_id = r.team_id
			 AND subject_entity.entity_id = r.subject_entity_id
			 AND subject_entity.space_id = r.space_id
			 AND subject_entity.space_generation = r.space_generation
			 AND subject_entity.status = 'active'
			LEFT JOIN entity_records object_entity
			  ON object_entity.team_id = r.team_id
			 AND object_entity.entity_id = r.object_entity_id
			 AND object_entity.space_id = r.space_id
			 AND object_entity.space_generation = r.space_generation
			 AND object_entity.status = 'active'
			LEFT JOIN entity_names subject_name
			  ON subject_name.team_id = r.team_id
			 AND subject_name.entity_id = r.subject_entity_id
			 AND subject_name.space_id = r.space_id
			 AND subject_name.space_generation = r.space_generation
			 AND subject_name.name_kind = 'canonical'
			 AND subject_name.valid_to IS NULL
			LEFT JOIN entity_names object_name
			  ON object_name.team_id = r.team_id
			 AND object_name.entity_id = r.object_entity_id
			 AND object_name.space_id = r.space_id
			 AND object_name.space_generation = r.space_generation
			 AND object_name.name_kind = 'canonical'
			 AND object_name.valid_to IS NULL
			LEFT JOIN value_records value
			  ON value.team_id = r.team_id
			 AND value.value_id = r.object_value_id
			 AND value.space_id = r.space_id
			 AND value.space_generation = r.space_generation
			LEFT JOIN effective_support support
			  ON support.relationship_id = r.relationship_id
			LEFT JOIN eligible_pending pending
			  ON pending.relationship_id = r.relationship_id
			WHERE r.team_id = ?::uuid
			  AND r.space_id = dense_mem_team_shared_space(r.team_id)
			  AND r.space_generation = dense_mem_team_shared_generation(r.team_id)
			  AND r.identity_alias_of_relationship_id IS NULL
			  AND (
			    (r.status = 'active' AND COALESCE(support.support_count, 0) > 0)
			    OR (r.status = 'pending_evidence' AND pending.relationship_id IS NOT NULL)
			  )
			  AND NOT EXISTS (
			    SELECT 1
			    FROM relationship_cross_references cross_reference
			    WHERE cross_reference.team_id = r.team_id
			      AND cross_reference.space_id = r.space_id
			      AND cross_reference.space_generation = r.space_generation
			      AND cross_reference.target_relationship_id = r.relationship_id
			      AND cross_reference.kind = 'challenges'
			  )
			  AND NOT EXISTS (
			    SELECT 1
			    FROM review_tasks review
			    WHERE review.team_id = r.team_id
			      AND review.space_id = r.space_id
			      AND review.space_generation = r.space_generation
			      AND review.relationship_id = r.relationship_id
			      AND review.status IN ('open', 'acknowledged')
			  )
			  AND (r.object_entity_id IS NULL OR object_entity.entity_id IS NOT NULL)
			ORDER BY r.updated_at DESC, r.relationship_id
			LIMIT ?
		`, input.TeamID, input.TeamID, input.TeamID, input.TeamID, input.Limit).Rows()
		if err != nil {
			return err
		}
		candidates := make([]DreamInput, 0, input.Limit)
		for rows.Next() {
			var item DreamInput
			if err := rows.Scan(
				&item.RelationshipID,
				&item.OwnerProfileID,
				&item.Version,
				&item.Status,
				&item.SubjectEntityID,
				&item.SubjectName,
				&item.SubjectKind,
				&item.PredicateKey,
				&item.PredicateVersion,
				&item.ObjectEntityID,
				&item.ObjectValueID,
				&item.ObjectName,
				&item.ObjectKind,
				&item.RelationshipKind,
				&item.CurrentCardinality,
			); err != nil {
				return err
			}
			candidates = append(candidates, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		evidenceByRelationship, err := listDreamInputEvidenceBatch(ctx, tx, input.TeamID, candidates)
		if err != nil {
			return err
		}
		for _, item := range candidates {
			evidence := evidenceByRelationship[item.RelationshipID]
			if len(evidence) == 0 {
				continue
			}
			item.Evidence = evidence
			inputs = append(inputs, item)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: list inputs: %w", err)
	}
	return inputs, nil
}

func (r *SemanticRepositoryImpl) ListDreamTargetPredicates(ctx context.Context, teamID string) ([]DreamTargetPredicate, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	predicates := []DreamTargetPredicate{}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT DISTINCT ON (predicate_key)
			       predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid
			  AND lifecycle_state = 'active'
			ORDER BY predicate_key, version DESC
		`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var predicate DreamTargetPredicate
			var subjectKinds pq.StringArray
			var objectKinds pq.StringArray
			if err := rows.Scan(
				&predicate.PredicateKey,
				&predicate.Version,
				&subjectKinds,
				&objectKinds,
				&predicate.RelationshipKind,
				&predicate.CurrentCardinality,
			); err != nil {
				return err
			}
			predicate.AllowedSubjectKinds = append([]string(nil), subjectKinds...)
			predicate.AllowedObjectKinds = append([]string(nil), objectKinds...)
			predicates = append(predicates, predicate)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("dream: list target predicates: %w", err)
	}
	return predicates, nil
}

func (r *SemanticRepositoryImpl) ListHypotheses(
	ctx context.Context,
	input ListHypothesesInput,
) ([]HypothesisRecord, string, error) {
	input = normalizeListHypothesesInput(input)
	if err := validateListHypothesesInput(input); err != nil {
		return nil, "", err
	}
	offset := evaluationCursorOffset(input.Cursor)
	limit := input.Limit + 1
	records := []HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND (? = '' OR status = ?)
			ORDER BY ` + hypothesisListOrder(input.Sort, input.Direction) + `, hypothesis_id
			LIMIT ? OFFSET ?
		`)
		rows, err := tx.WithContext(ctx).Raw(
			query,
			input.TeamID,
			input.Status,
			input.Status,
			limit,
			offset,
		).Rows()
		if err != nil {
			return err
		}
		records, err = scanHypothesisRecords(rows)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return hydrateDreamHypothesisDerivations(ctx, tx, input.TeamID, records)
	})
	if err != nil {
		return nil, "", fmt.Errorf("dream: list hypotheses: %w", err)
	}
	next := ""
	if len(records) > input.Limit {
		records = records[:input.Limit]
		next = fmt.Sprintf("%d", offset+input.Limit)
	}
	return records, next, nil
}

func (r *SemanticRepositoryImpl) GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.HypothesisID = strings.TrimSpace(input.HypothesisID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.HypothesisID); err != nil {
		return nil, fmt.Errorf("hypothesis_id is required: %w", err)
	}
	var record *HypothesisRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
				WHERE team_id = ?::uuid
				        AND space_id = dense_mem_team_shared_space(team_id)
				        AND space_generation = dense_mem_team_shared_generation(team_id)
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
			LIMIT 1
		`), input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		records := []HypothesisRecord{*loaded}
		if err := hydrateDreamHypothesisDerivations(ctx, tx, input.TeamID, records); err != nil {
			return err
		}
		record = &records[0]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: get hypothesis: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 {
		input.Limit = 5
	}
	if input.Limit > 20 {
		input.Limit = 20
	}
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	pattern := "%" + input.Query + "%"
	records := []HypothesisRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		query := hypothesisSelectSQL(`
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND status IN ('proposed', 'reinforced')
			  AND NOT (` + hypothesisSourceIneligiblePredicateSQL + `)
			  AND (? = '' OR statement ILIKE ? OR rationale ILIKE ?)
			ORDER BY CASE WHEN ? <> '' AND statement ILIKE ? THEN 0 ELSE 1 END,
			         updated_at DESC,
			         hypothesis_id
			LIMIT ?
		`)
		rows, err := tx.WithContext(ctx).Raw(query, input.TeamID, input.Query, pattern, pattern, input.Query, pattern, input.Limit).Rows()
		if err != nil {
			return err
		}
		records, err = scanHypothesisRecords(rows)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return hydrateDreamHypothesisDerivations(ctx, tx, input.TeamID, records)
	})
	if err != nil {
		return nil, fmt.Errorf("dream: recall hypotheses: %w", err)
	}
	return records, nil
}

func (r *SemanticRepositoryImpl) UpdateHypothesisStatus(
	ctx context.Context,
	input UpdateHypothesisStatusInput,
) (*HypothesisRecord, error) {
	input = normalizeUpdateHypothesisStatusInput(input)
	if err := validateUpdateHypothesisStatusInput(input); err != nil {
		return nil, err
	}
	var record *HypothesisRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.ActorProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(hypothesisUpdateReturningSQL(`
			UPDATE hypotheses
			SET status = ?,
			    invalidated_reason = CASE WHEN ? <> '' THEN ? ELSE invalidated_reason END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
				WHERE team_id = ?::uuid
				        AND space_id = dense_mem_team_shared_space(team_id)
				        AND space_generation = dense_mem_team_shared_generation(team_id)
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
		`), input.Status, input.InvalidatedReason, input.InvalidatedReason,
			input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := insertHypothesisFeedbackEvent(ctx, tx, input.TeamID, loaded.HypothesisID,
			input.ActorProfileID, input.Decision, input.InvalidatedReason, ""); err != nil {
			return err
		}
		record = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: update hypothesis: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) SubmitHypothesis(
	ctx context.Context,
	input SubmitHypothesisInput,
) (*HypothesisRecord, error) {
	input = normalizeSubmitHypothesisInput(input)
	if err := validateSubmitHypothesisInput(input); err != nil {
		return nil, err
	}
	var record *HypothesisRecord
	err := r.withTeamProfileTx(ctx, input.TeamID, input.ActorProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(hypothesisUpdateReturningSQL(`
			UPDATE hypotheses
			SET status = 'submitted',
			    submitted_ingest_id = ?::uuid,
			    submitted_at = now(),
			    invalidated_reason = CASE WHEN ? <> '' THEN ? ELSE invalidated_reason END,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
			  AND hypothesis_id = COALESCE((
			      SELECT canonical_hypothesis_id
			      FROM hypotheses
				WHERE team_id = ?::uuid
				        AND space_id = dense_mem_team_shared_space(team_id)
				        AND space_generation = dense_mem_team_shared_generation(team_id)
			        AND hypothesis_id = ?::uuid
			  ), ?::uuid)
		`), input.SubmittedIngestID, input.InvalidatedReason, input.InvalidatedReason,
			input.TeamID, input.TeamID, input.HypothesisID, input.HypothesisID).Rows()
		if err != nil {
			return err
		}
		if !rows.Next() {
			_ = rows.Close()
			return ErrDreamHypothesisNotFound
		}
		loaded, err := scanHypothesisRecord(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := insertHypothesisFeedbackEvent(ctx, tx, input.TeamID, loaded.HypothesisID,
			input.ActorProfileID, input.Decision, input.InvalidatedReason, input.SubmittedIngestID); err != nil {
			return err
		}
		record = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dream: submit hypothesis: %w", err)
	}
	return record, nil
}

func validateHypothesisEndpoints(ctx context.Context, tx *gorm.DB, input UpsertHypothesisInput) error {
	predicate, err := loadPredicateDefinition(ctx, tx, input.TeamID, input.PredicateKey, input.PredicateVersion)
	if err != nil {
		return err
	}
	if err := validateHypothesisEndpointKinds(ctx, tx, input, predicate); err != nil {
		return err
	}
	var existing int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND space_id = dense_mem_team_shared_space(team_id)
		  AND space_generation = dense_mem_team_shared_generation(team_id)
		  AND identity_alias_of_relationship_id IS NULL
		  AND subject_entity_id = ?::uuid
		  AND lower(btrim(predicate_key)) = lower(btrim(?))
		  AND object_entity_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND object_value_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
	`, input.TeamID, input.SubjectEntityID, input.PredicateKey,
		input.ObjectEntityID, input.ObjectValueID).Scan(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return ErrDreamExactRelationshipExists
	}
	return nil
}

func validateHypothesisTargetAbsent(ctx context.Context, tx *gorm.DB, input UpsertHypothesisInput) error {
	var existing int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM hypotheses
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
		  AND (
		    target_identity = ?
		    OR (
		      target_identity IS NULL
		      AND subject_entity_id = ?::uuid
		      AND lower(btrim(predicate_key)) = lower(btrim(?))
		      AND object_entity_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		      AND object_value_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		    )
		  )
	`, input.TeamID, input.TargetIdentity, input.SubjectEntityID, input.PredicateKey,
		input.ObjectEntityID, input.ObjectValueID).Scan(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return ErrDreamExactHypothesisExists
	}
	return nil
}

func marshalIntMapJSON(value map[string]int) ([]byte, error) {
	if value == nil {
		value = map[string]int{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func hypothesisTargetIdentity(teamID, subjectEntityID, predicateKey, objectEntityID, objectValueID string) string {
	object := "value:" + strings.TrimSpace(objectValueID)
	if strings.TrimSpace(objectEntityID) != "" {
		object = "entity:" + strings.TrimSpace(objectEntityID)
	}
	raw := strings.Join([]string{
		strings.TrimSpace(teamID),
		strings.TrimSpace(subjectEntityID),
		strings.ToLower(strings.TrimSpace(predicateKey)),
		object,
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func reinforceHypothesisByHash(
	ctx context.Context,
	tx *gorm.DB,
	input UpsertHypothesisInput,
) (*HypothesisRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(hypothesisUpdateReturningSQL(`
		UPDATE hypotheses
		SET status = CASE
		        WHEN status IN ('proposed', 'reinforced') THEN 'reinforced'
		        ELSE status
		    END,
		    updated_at = now()
			WHERE team_id = ?::uuid
			  AND space_id = dense_mem_team_shared_space(team_id)
			  AND space_generation = dense_mem_team_shared_generation(team_id)
			  AND canonical_hypothesis_id IS NULL
		  AND content_hash = ?
	`), input.TeamID, input.ContentHash).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrDreamHypothesisNotFound
	}
	return scanHypothesisRecord(rows)
}

func loadDreamCycleByWindow(ctx context.Context, tx *gorm.DB, teamID, windowKey string) (*DreamCycleRun, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT `+dreamCycleRunSelectColumns+`
		FROM dream_cycle_runs
		WHERE team_id = ?::uuid
		  AND space_id = dense_mem_team_shared_space(team_id)
		  AND space_generation = dense_mem_team_shared_generation(team_id)
		  AND canonical_run_id IS NULL
		  AND window_key = ?
		LIMIT 1
	`, teamID, windowKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrDreamCycleAlreadyClaimed
	}
	return scanDreamCycleRun(rows)
}
