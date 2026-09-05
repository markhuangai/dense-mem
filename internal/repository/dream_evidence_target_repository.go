package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/domain"
	"gorm.io/gorm"
)

// EvidenceDiscoveryRepository is the scheduler-only port for evidence lane
// selection and per-target durable evaluation.
type EvidenceDiscoveryRepository interface {
	ListEvidenceDiscoveryTargets(context.Context, string, int, int) ([]EvidenceDiscoveryTargetInput, error)
	LoadEvidenceDiscoveryRunTotals(context.Context, string, string) (EvidenceDiscoveryRunTotals, error)
	PersistEvidenceDiscoveryEvaluation(context.Context, EvidenceDiscoveryEvaluationInput) (DreamGenerationPersistResult, error)
	WithEvidenceDiscoveryTargetLock(context.Context, string, string, string, func(EvidenceDiscoveryAttempt) error) error
	MarkEvidenceDiscoveryAttemptDispatched(context.Context, EvidenceDiscoveryAttemptValidationInput) error
	MarkEvidenceDiscoveryAttemptValidated(context.Context, EvidenceDiscoveryAttemptValidationInput) error
	AbandonEvidenceDiscoveryAttempt(context.Context, string, string, string) error
}

var _ EvidenceDiscoveryRepository = (*SemanticRepositoryImpl)(nil)

func (r *SemanticRepositoryImpl) ListEvidenceDiscoveryTargets(
	ctx context.Context,
	teamID string,
	limit int,
	maxContexts int,
) ([]EvidenceDiscoveryTargetInput, error) {
	teamID = strings.TrimSpace(teamID)
	if limit <= 0 {
		limit = 20
	}
	if limit > 20 {
		limit = 20
	}
	if maxContexts <= 0 {
		maxContexts = 10
	}
	if maxContexts > 10 {
		maxContexts = 10
	}
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	var loadedEvidenceDiscoveryTargets []EvidenceDiscoveryTargetInput
	load := func() error {
		result := []EvidenceDiscoveryTargetInput{}
		err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
			predicates, err := listEvidenceDiscoveryPredicates(ctx, tx, teamID, 100)
			if err != nil {
				return err
			}
			rows, err := tx.WithContext(ctx).Raw(`
			WITH latest_security AS (
				SELECT DISTINCT ON (security.team_id, security.fragment_id)
				       security.team_id, security.fragment_id, security.decision
				FROM evidence_security_events security
				WHERE security.team_id = ?::uuid
				ORDER BY security.team_id, security.fragment_id, security.created_at DESC, security.security_event_id DESC
			),
			eligible AS (
				SELECT fragment.team_id,
				       fragment.fragment_id,
				       fragment.space_id,
				       fragment.space_generation,
				       fragment.content_hash,
				       COALESCE(fragment.source_id::text, '') AS source_id,
				       COALESCE(fragment.source_revision_id::text, '') AS source_revision_id,
				       CASE
				           WHEN fragment.source_id IS NOT NULL THEN 'source:' || fragment.source_id::text
				           WHEN btrim(fragment.source_ref) <> '' THEN 'owner:' || fragment.owner_profile_id::text || ':' || fragment.source_ref
				           ELSE 'ingest:' || fragment.ingest_id::text
				       END AS source_group_key,
				       fragment.authority,
				       fragment.content,
				       char_length(fragment.content) AS span_end,
				       fragment.created_at
				FROM evidence_fragments fragment
				JOIN knowledge_ingests ingest
				  ON ingest.team_id = fragment.team_id
				 AND ingest.ingest_id = fragment.ingest_id
				JOIN teams team
				  ON team.id = fragment.team_id
				JOIN search_documents document
				  ON document.team_id = fragment.team_id
				 AND document.source_kind = 'evidence'
				 AND document.source_id = fragment.fragment_id
				 AND document.source_version = 1
				 AND document.search_state = 'current'
				 AND document.embedding IS NOT NULL
				 AND document.document_hash = regexp_replace(fragment.content_hash, '^sha256:', '')
				LEFT JOIN evidence_sources source
				  ON source.team_id = fragment.team_id
				 AND source.source_id = fragment.source_id
				 AND source.space_id = fragment.space_id
				 AND source.space_generation = fragment.space_generation
				WHERE fragment.team_id = ?::uuid
				  AND team.status = 'active'
				  AND team.deleted_at IS NULL
				  AND fragment.space_id = dense_mem_team_shared_space(fragment.team_id)
				  AND fragment.space_generation = dense_mem_team_shared_generation(fragment.team_id)
				  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
				  AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
				  AND NOT EXISTS (
				      SELECT 1 FROM evidence_exact_aliases alias
				      WHERE alias.team_id = fragment.team_id
				        AND alias.alias_fragment_id = fragment.fragment_id
				  )
				  AND NOT EXISTS (
				      SELECT 1 FROM evidence_quarantines quarantine
				      WHERE quarantine.team_id = fragment.team_id
				        AND quarantine.fragment_id = fragment.fragment_id
				        AND quarantine.space_id = fragment.space_id
				        AND quarantine.space_generation = fragment.space_generation
				        AND quarantine.status = 'active'
				  )
				  AND NOT EXISTS (
				      SELECT 1 FROM evidence_lifecycle_events lifecycle
				      WHERE lifecycle.team_id = fragment.team_id
				        AND lifecycle.target_fragment_id = fragment.fragment_id
				        AND lifecycle.space_id = fragment.space_id
				        AND lifecycle.space_generation = fragment.space_generation
				  )
				  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
				  AND EXISTS (
				      SELECT 1
				      FROM latest_security security
				      WHERE security.team_id = fragment.team_id
				        AND security.fragment_id = fragment.fragment_id
				        AND security.decision IN ('pass', 'released')
				  )
			),
			attempt_summary AS (
				SELECT team_id, target_evidence_id, target_content_hash,
				       COUNT(*) FILTER (WHERE status = 'validated')::int AS validated_count,
			       COUNT(*) FILTER (WHERE status = 'reserved'
			           AND (reservation_expires_at > now() OR dispatch_started_at IS NOT NULL))::int AS reserved_count,
				       COALESCE(MAX(created_hypotheses) FILTER (WHERE status = 'validated' AND pass_number = 1), 0)::int AS first_created,
				       COALESCE(MAX(accepted_proposals) FILTER (WHERE status = 'validated' AND pass_number = 1), 0)::int AS first_accepted,
				       COALESCE(bool_or(status = 'validated' AND pass_number = 1 AND evaluation_persisted), false) AS first_persisted,
				       MAX(updated_at) AS last_attempt_at
				FROM dream_evidence_target_attempts
				WHERE team_id = ?::uuid
				GROUP BY team_id, target_evidence_id, target_content_hash
			)
			SELECT eligible.fragment_id::text,
			       eligible.space_id::text,
			       eligible.space_generation,
			       eligible.content_hash,
			       eligible.source_id,
			       eligible.source_revision_id,
			       eligible.source_group_key,
			       eligible.authority,
			       eligible.content,
			       eligible.span_end,
			       eligible.created_at,
			       COALESCE(MAX(evaluation.created_at), MAX(attempt.last_attempt_at)) AS last_evaluated_at,
			       GREATEST(COUNT(evaluation.evaluation_id)::int, COALESCE(MAX(attempt.validated_count), 0)) AS pass_count
			FROM eligible
			LEFT JOIN dream_evidence_target_evaluations evaluation
			  ON evaluation.team_id = eligible.team_id
			 AND evaluation.target_evidence_id = eligible.fragment_id
			 AND evaluation.target_content_hash = eligible.content_hash
			LEFT JOIN attempt_summary attempt
			  ON attempt.team_id = eligible.team_id
			 AND attempt.target_evidence_id = eligible.fragment_id
			 AND attempt.target_content_hash = eligible.content_hash
			GROUP BY eligible.fragment_id, eligible.space_id, eligible.space_generation,
			         eligible.content_hash, eligible.source_id, eligible.source_revision_id,
			         eligible.source_group_key, eligible.authority, eligible.content,
			         eligible.span_end, eligible.created_at
			HAVING COALESCE(MAX(attempt.reserved_count), 0) = 0
			   AND (
				   (COUNT(evaluation.evaluation_id) = 0 AND COALESCE(MAX(attempt.validated_count), 0) = 0)
				   OR (
				       GREATEST(COUNT(evaluation.evaluation_id)::int, COALESCE(MAX(attempt.validated_count), 0)) = 1
				       AND (
				           (COALESCE(bool_or(attempt.first_persisted), false) AND COALESCE(MAX(attempt.first_created), 0) > 0)
				           OR (NOT COALESCE(bool_or(attempt.first_persisted), false) AND COALESCE(MAX(attempt.first_accepted), 0) > 0)
				           OR (COUNT(evaluation.evaluation_id) = 1 AND COALESCE(MAX(attempt.validated_count), 0) = 0 AND COALESCE(MAX(evaluation.created_hypotheses), 0) > 0)
				       )
				   )
			   )
			ORDER BY CASE WHEN GREATEST(COUNT(evaluation.evaluation_id)::int, COALESCE(MAX(attempt.validated_count), 0)) = 0 THEN 0 ELSE 1 END,
			         COALESCE(MAX(evaluation.created_at), MAX(attempt.last_attempt_at)) NULLS FIRST,
			         eligible.created_at,
			         eligible.fragment_id
			LIMIT ?
		`, teamID, teamID, teamID, limit).Rows()
			if err != nil {
				return err
			}
			// The transaction has one PostgreSQL connection, so close this result
			// before issuing each per-target context query below.
			targetRows := make([]EvidenceTarget, 0, limit)
			for rows.Next() {
				var target EvidenceTarget
				var lastEvaluated sql.NullTime
				if err := rows.Scan(
					&target.FragmentID,
					&target.SpaceID,
					&target.SpaceGeneration,
					&target.ContentHash,
					&target.SourceID,
					&target.SourceRevisionID,
					&target.SourceGroupKey,
					&target.Authority,
					&target.Content,
					&target.SpanEnd,
					&target.CreatedAt,
					&lastEvaluated,
					&target.PassCount,
				); err != nil {
					_ = rows.Close()
					return err
				}
				target.EvidenceID = target.FragmentID
				target.SpanStart = 0
				if lastEvaluated.Valid {
					value := lastEvaluated.Time
					target.LastEvaluatedAt = &value
				}
				targetRows = append(targetRows, target)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, target := range targetRows {
				contexts, err := listEvidenceDiscoveryContexts(ctx, tx, teamID, target, maxContexts-1)
				if err != nil {
					return err
				}
				allContexts := make([]EvidenceContext, 0, len(contexts)+1)
				allContexts = append(allContexts, EvidenceContext{
					EvidenceID: target.EvidenceID, FragmentID: target.FragmentID,
					SourceID: target.SourceID, SourceRevisionID: target.SourceRevisionID,
					SourceGroupKey: target.SourceGroupKey, Authority: target.Authority,
					Content: target.Content,
				})
				allContexts = append(allContexts, contexts...)
				nodes, err := listEvidenceDiscoveryNodes(ctx, tx, teamID, target, allContexts, 100)
				if err != nil {
					return err
				}
				result = append(result, EvidenceDiscoveryTargetInput{
					Target: target, Contexts: allContexts,
					Nodes: nodes, AllowedPredicates: predicates,
				})
			}
			return nil
		})
		if err == nil {
			loadedEvidenceDiscoveryTargets = result
		}
		return err
	}
	err := load()
	if errors.Is(err, driver.ErrBadConn) {
		err = load()
	}
	if err != nil {
		return nil, fmt.Errorf("dream: list evidence targets: %w", err)
	}
	return loadedEvidenceDiscoveryTargets, nil
}

func (r *SemanticRepositoryImpl) LoadEvidenceDiscoveryRunTotals(
	ctx context.Context,
	teamID string,
	runID string,
) (EvidenceDiscoveryRunTotals, error) {
	teamID = strings.TrimSpace(teamID)
	runID = strings.TrimSpace(runID)
	if _, err := uuid.Parse(teamID); err != nil {
		return EvidenceDiscoveryRunTotals{}, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(runID); err != nil {
		return EvidenceDiscoveryRunTotals{}, fmt.Errorf("run_id is required: %w", err)
	}
	var totals EvidenceDiscoveryRunTotals
	err := r.withDreamWriteTx(ctx, teamID, "", true, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT COUNT(*)::int,
			       COUNT(DISTINCT target_evidence_id)::int,
			       COALESCE(SUM(created_hypotheses), 0)::int,
			       COALESCE(SUM(rejected_proposals), 0)::int,
			       COALESCE(SUM(provider_proposals), 0)::int,
			       COALESCE(SUM(provider_turns), 0)::int,
			       COALESCE(SUM(provider_input_tokens), 0)::int,
			       COALESCE(SUM(provider_output_tokens), 0)::int
			FROM dream_evidence_target_evaluations
			WHERE team_id = ?::uuid AND run_id = ?::uuid
		`, teamID, runID).Row().Scan(
			&totals.Evaluated,
			&totals.TargetCount,
			&totals.Created,
			&totals.Rejected,
			&totals.ProviderProposals,
			&totals.ProviderTurns,
			&totals.ProviderInputTokens,
			&totals.ProviderOutputTokens,
		)
	})
	if err != nil {
		return EvidenceDiscoveryRunTotals{}, fmt.Errorf("dream: load evidence discovery run totals: %w", err)
	}
	return totals, nil
}

func listEvidenceDiscoveryNodes(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	target EvidenceTarget,
	contexts []EvidenceContext,
	limit int,
) ([]EvidenceNode, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	evidenceText := make([]string, 0, len(contexts)+1)
	evidenceText = append(evidenceText, target.Content)
	for _, context := range contexts {
		evidenceText = append(evidenceText, context.Content)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH node AS (
			SELECT entity.entity_id::text AS id,
			       COALESCE(name.display_name, '') AS display,
			       entity.entity_kind AS kind,
			       entity.created_at
			FROM entity_records entity
			LEFT JOIN entity_names name
			  ON name.team_id = entity.team_id
			 AND name.entity_id = entity.entity_id
			 AND name.name_kind = 'canonical'
			 AND name.valid_to IS NULL
			WHERE entity.team_id = ?::uuid
			  AND entity.space_id = dense_mem_team_shared_space(entity.team_id)
			  AND entity.space_generation = dense_mem_team_shared_generation(entity.team_id)
			  AND entity.status = 'active'
			UNION ALL
			SELECT value.value_id::text AS id,
			       COALESCE(NULLIF(value.display, ''), value.canonical_value) AS display,
			       value.value_type AS kind,
			       value.created_at
			FROM value_records value
			WHERE value.team_id = ?::uuid
			  AND value.space_id = dense_mem_team_shared_space(value.team_id)
			  AND value.space_generation = dense_mem_team_shared_generation(value.team_id)
		), evidence_text AS (
			SELECT unnest(?::text[]) AS content
		)
		SELECT node.id, node.display, node.kind
		FROM node
		ORDER BY CASE
			WHEN btrim(node.display) <> '' AND EXISTS (
				SELECT 1
				FROM evidence_text
				WHERE strpos(lower(evidence_text.content), lower(node.display)) > 0
			) THEN 0
			ELSE 1
		END,
		node.created_at, node.id
		LIMIT ?
	`, teamID, teamID, pq.Array(evidenceText), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]EvidenceNode, 0, limit)
	for rows.Next() {
		var node EvidenceNode
		if err := rows.Scan(&node.ID, &node.Display, &node.Kind); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func listEvidenceDiscoveryPredicates(ctx context.Context, tx *gorm.DB, teamID string, limit int) ([]DreamTargetPredicate, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality
		FROM (
			SELECT DISTINCT ON (predicate_key)
			       predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid
			  AND lifecycle_state = 'active'
			ORDER BY predicate_key, version DESC
		) predicates
		ORDER BY predicate_key, version
		LIMIT ?
	`, teamID, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	predicates := make([]DreamTargetPredicate, 0, limit)
	for rows.Next() {
		var predicate DreamTargetPredicate
		var subjectKinds, objectKinds pq.StringArray
		if err := rows.Scan(&predicate.PredicateKey, &predicate.Version, &subjectKinds, &objectKinds,
			&predicate.RelationshipKind, &predicate.CurrentCardinality); err != nil {
			return nil, err
		}
		predicate.AllowedSubjectKinds = append([]string(nil), subjectKinds...)
		predicate.AllowedObjectKinds = append([]string(nil), objectKinds...)
		predicates = append(predicates, predicate)
	}
	return predicates, rows.Err()
}

func listEvidenceDiscoveryContexts(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	target EvidenceTarget,
	limit int,
) ([]EvidenceContext, error) {
	if limit <= 0 {
		return []EvidenceContext{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest_security AS (
			SELECT DISTINCT ON (security.team_id, security.fragment_id)
			       security.team_id, security.fragment_id, security.decision
			FROM evidence_security_events security
			WHERE security.team_id = ?::uuid
			ORDER BY security.team_id, security.fragment_id, security.created_at DESC, security.security_event_id DESC
		), target_document AS (
			SELECT embedding
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND source_kind = 'evidence'
			  AND source_id = ?::uuid
			  AND source_version = 1
			  AND search_state = 'current'
			  AND embedding IS NOT NULL
			LIMIT 1
		)
		SELECT fragment.fragment_id::text,
		       COALESCE(fragment.source_id::text, ''),
		       COALESCE(fragment.source_revision_id::text, ''),
		       CASE
		           WHEN fragment.source_id IS NOT NULL THEN 'source:' || fragment.source_id::text
		           WHEN btrim(fragment.source_ref) <> '' THEN 'owner:' || fragment.owner_profile_id::text || ':' || fragment.source_ref
		           ELSE 'ingest:' || fragment.ingest_id::text
		       END,
		       fragment.authority,
		       fragment.content
		FROM evidence_fragments fragment
		JOIN knowledge_ingests ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		JOIN search_documents document
		  ON document.team_id = fragment.team_id
		 AND document.source_kind = 'evidence'
		 AND document.source_id = fragment.fragment_id
		 AND document.source_version = 1
		 AND document.search_state = 'current'
		 AND document.embedding IS NOT NULL
		 AND document.document_hash = regexp_replace(fragment.content_hash, '^sha256:', '')
		CROSS JOIN target_document target_doc
		LEFT JOIN evidence_sources source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		 AND source.space_id = fragment.space_id
		 AND source.space_generation = fragment.space_generation
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id <> ?::uuid
		  AND fragment.space_id = dense_mem_team_shared_space(fragment.team_id)
		  AND fragment.space_generation = dense_mem_team_shared_generation(fragment.team_id)
		  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_exact_aliases alias
		      WHERE alias.team_id = fragment.team_id AND alias.alias_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_quarantines quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.space_id = fragment.space_id
		        AND quarantine.space_generation = fragment.space_generation
		        AND quarantine.status = 'active'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_lifecycle_events lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		        AND lifecycle.space_id = fragment.space_id
		        AND lifecycle.space_generation = fragment.space_generation
		  )
		  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
		  AND EXISTS (
		      SELECT 1
		      FROM latest_security security
		      WHERE security.team_id = fragment.team_id
		        AND security.fragment_id = fragment.fragment_id
		        AND security.decision IN ('pass', 'released')
		  )
		ORDER BY CASE
		           WHEN fragment.source_id IS NOT NULL
		                AND NULLIF(?, '')::uuid = fragment.source_id THEN 0
		           WHEN fragment.source_id IS NULL AND ? <> ''
		                AND (CASE WHEN btrim(fragment.source_ref) <> ''
		                          THEN 'owner:' || fragment.owner_profile_id::text || ':' || fragment.source_ref
		                          ELSE 'ingest:' || fragment.ingest_id::text END) = ? THEN 0
		           ELSE 1
		         END,
		         document.embedding <=> target_doc.embedding,
		         fragment.created_at DESC,
		         fragment.fragment_id
		LIMIT ?
	`, teamID, teamID, target.FragmentID, teamID, target.FragmentID,
		target.SourceID, target.SourceGroupKey, target.SourceGroupKey, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	contexts := make([]EvidenceContext, 0, limit)
	for rows.Next() {
		var context EvidenceContext
		if err := rows.Scan(
			&context.FragmentID,
			&context.SourceID,
			&context.SourceRevisionID,
			&context.SourceGroupKey,
			&context.Authority,
			&context.Content,
		); err != nil {
			return nil, err
		}
		context.EvidenceID = context.FragmentID
		contexts = append(contexts, context)
	}
	return contexts, rows.Err()
}

func (r *SemanticRepositoryImpl) PersistEvidenceDiscoveryEvaluation(
	ctx context.Context,
	input EvidenceDiscoveryEvaluationInput,
) (DreamGenerationPersistResult, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.LeaseToken = strings.TrimSpace(input.LeaseToken)
	input.AttemptID = strings.TrimSpace(input.AttemptID)
	input.ReservationToken = strings.TrimSpace(input.ReservationToken)
	input.ProviderModel = strings.TrimSpace(input.ProviderModel)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RunID); err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("run_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.LeaseToken); err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("lease_token is required: %w", err)
	}
	if _, err := uuid.Parse(input.AttemptID); err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("attempt_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReservationToken); err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("reservation_token is required: %w", err)
	}
	if input.PassNumber < 1 || input.PassNumber > 2 {
		return DreamGenerationPersistResult{}, errors.New("pass_number must be one or two")
	}
	if input.ProviderModel == "" {
		return DreamGenerationPersistResult{}, errors.New("provider_model is required")
	}
	if input.ProviderTurns < 0 || input.ProviderInputTokens < 0 || input.ProviderOutputTokens < 0 || input.ProviderProposals < 0 {
		return DreamGenerationPersistResult{}, errors.New("provider diagnostics must not be negative")
	}
	if input.AcceptedProposals < 0 || input.RejectedProposals < 0 || input.CreatedHypotheses < 0 {
		return DreamGenerationPersistResult{}, errors.New("proposal diagnostics must not be negative")
	}
	if input.Target.FragmentID == "" || input.Target.EvidenceID != input.Target.FragmentID {
		return DreamGenerationPersistResult{}, errors.New("target evidence identity is required")
	}
	result := DreamGenerationPersistResult{}
	err := r.withDreamWriteTx(ctx, input.TeamID, "", true, func(tx *gorm.DB) error {
		if err := requireCurrentDreamCycleLease(ctx, tx, input.TeamID, input.RunID, input.LeaseToken); err != nil {
			return err
		}
		if err := validateEvidenceDiscoveryTargetInTx(ctx, tx, input.TeamID, input.Target); err != nil {
			return err
		}
		var attemptPass int
		var attemptStatus string
		var attemptTargetID, attemptContentHash string
		if err := tx.WithContext(ctx).Raw(`
			SELECT pass_number, status, target_evidence_id::text, target_content_hash
			FROM dream_evidence_target_attempts
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid AND reservation_token = ?::uuid
			FOR UPDATE
		`, input.TeamID, input.AttemptID, input.ReservationToken).Row().Scan(
			&attemptPass, &attemptStatus, &attemptTargetID, &attemptContentHash,
		); errors.Is(err, sql.ErrNoRows) {
			return ErrEvidenceDiscoveryAttemptNotReserved
		} else if err != nil {
			return err
		}
		if attemptPass != input.PassNumber || attemptTargetID != input.Target.EvidenceID || attemptContentHash != input.Target.ContentHash ||
			(attemptStatus != "reserved" && attemptStatus != "validated") {
			return ErrEvidenceDiscoveryAttemptNotReserved
		}
		if attemptStatus == "reserved" {
			if err := tx.WithContext(ctx).Exec(`
				UPDATE dream_evidence_target_attempts
				SET status = 'validated', dispatch_started_at = COALESCE(dispatch_started_at, now()),
				    accepted_proposals = ?, validated_at = now(), updated_at = now()
				WHERE team_id = ?::uuid AND attempt_id = ?::uuid AND reservation_token = ?::uuid
			`, input.AcceptedProposals, input.TeamID, input.AttemptID, input.ReservationToken).Error; err != nil {
				return err
			}
		}
		var priorPasses int
		var priorCreated int
		if err := tx.WithContext(ctx).Raw(`
			SELECT COUNT(*)::int, COALESCE(SUM(created_hypotheses), 0)::int
			FROM dream_evidence_target_evaluations
			WHERE team_id = ?::uuid
			  AND target_evidence_id = ?::uuid
			  AND target_content_hash = ?
		`, input.TeamID, input.Target.EvidenceID, input.Target.ContentHash).Row().Scan(&priorPasses, &priorCreated); err != nil {
			return err
		}
		var attemptPasses, attemptCreated int
		if err := tx.WithContext(ctx).Raw(`
			SELECT COUNT(*) FILTER (WHERE status = 'validated')::int,
			       COALESCE(SUM(created_hypotheses) FILTER (WHERE status = 'validated'), 0)::int
			FROM dream_evidence_target_attempts
			WHERE team_id = ?::uuid
			  AND target_evidence_id = ?::uuid
			  AND target_content_hash = ?
			  AND attempt_id <> ?::uuid
		`, input.TeamID, input.Target.EvidenceID, input.Target.ContentHash, input.AttemptID).Row().Scan(&attemptPasses, &attemptCreated); err != nil {
			return err
		}
		if attemptPasses > priorPasses {
			priorPasses, priorCreated = attemptPasses, attemptCreated
		}
		var firstPersisted bool
		var firstAccepted int
		if err := tx.WithContext(ctx).Raw(`
			SELECT evaluation_persisted, accepted_proposals
			FROM dream_evidence_target_attempts
			WHERE team_id = ?::uuid AND target_evidence_id = ?::uuid
			  AND target_content_hash = ? AND attempt_id <> ?::uuid
			  AND pass_number = 1 AND status = 'validated'
			LIMIT 1
		`, input.TeamID, input.Target.EvidenceID, input.Target.ContentHash, input.AttemptID).Row().Scan(&firstPersisted, &firstAccepted); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if priorPasses+1 != input.PassNumber {
			return fmt.Errorf("evidence target pass %d is not next after %d", input.PassNumber, priorPasses)
		}
		if input.PassNumber == 2 && firstPersisted && priorCreated == 0 {
			return errors.New("evidence target with no first-pass hypothesis is closed")
		}
		if input.PassNumber == 2 && !firstPersisted && firstAccepted == 0 {
			return errors.New("evidence target with no first-pass proposal is closed")
		}
		for index := range input.Proposals {
			input.Proposals[index].TeamID = input.TeamID
			input.Proposals[index].RunID = input.RunID
			input.Proposals[index].CreatedByProfileID = ""
			input.Proposals[index].Lane = domain.DreamLaneEvidenceDiscovery
			input.Proposals[index] = normalizeUpsertHypothesisInput(input.Proposals[index])
			if err := validateEvidenceDiscoveryProposalInTx(ctx, tx, input.TeamID, input.Target.EvidenceID, input.Proposals[index]); err != nil {
				return fmt.Errorf("proposals[%d]: %w", index, err)
			}
			record, inserted, err := insertHypothesisTx(ctx, tx, input.Proposals[index])
			_ = record
			if err != nil {
				if errors.Is(err, ErrDreamExactRelationshipExists) || errors.Is(err, ErrDreamExactHypothesisExists) {
					result.Rejected++
					continue
				}
				return err
			}
			if inserted {
				result.Created++
			}
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO dream_evidence_target_evaluations (
			    team_id, run_id, space_id, space_generation, target_evidence_id,
				target_content_hash, pass_number, provider_model, provider_turns,
				provider_input_tokens, provider_output_tokens, provider_proposals, accepted_proposals,
				rejected_proposals, created_hypotheses
			)
			VALUES (?, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, input.TeamID, input.RunID, input.Target.SpaceID, input.Target.SpaceGeneration,
			input.Target.EvidenceID, input.Target.ContentHash, input.PassNumber, input.ProviderModel,
			input.ProviderTurns, input.ProviderInputTokens, input.ProviderOutputTokens,
			input.ProviderProposals, len(input.Proposals), input.RejectedProposals+result.Rejected, result.Created).Error; err != nil {
			return err
		}
		updateAttempt := tx.WithContext(ctx).Exec(`
			UPDATE dream_evidence_target_attempts
			SET status = 'validated', dispatch_started_at = COALESCE(dispatch_started_at, now()),
			    evaluation_persisted = true, accepted_proposals = ?, created_hypotheses = ?,
			    validated_at = COALESCE(validated_at, now()), updated_at = now()
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
			  AND reservation_token = ?::uuid AND pass_number = ? AND status = 'validated'
		`, len(input.Proposals), result.Created, input.TeamID, input.AttemptID, input.ReservationToken, input.PassNumber)
		if updateAttempt.Error != nil {
			return updateAttempt.Error
		}
		if updateAttempt.RowsAffected != 1 {
			return ErrEvidenceDiscoveryAttemptNotReserved
		}
		var persisted bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT evaluation_persisted
			FROM dream_evidence_target_attempts
			WHERE team_id = ?::uuid AND attempt_id = ?::uuid
		`, input.TeamID, input.AttemptID).Row().Scan(&persisted); err != nil {
			return err
		}
		if !persisted {
			return ErrEvidenceDiscoveryAttemptNotReserved
		}
		return nil
	})
	if err != nil {
		return DreamGenerationPersistResult{}, fmt.Errorf("dream: persist evidence evaluation: %w", err)
	}
	return result, nil
}

func validateEvidenceDiscoveryTargetInTx(ctx context.Context, tx *gorm.DB, teamID string, target EvidenceTarget) error {
	var contentHash string
	var spaceID string
	var spaceGeneration int64
	var sourceID, sourceRevisionID, sourceGroupKey, authority string
	err := tx.WithContext(ctx).Raw(`
		WITH latest_security AS (
			SELECT DISTINCT ON (security.team_id, security.fragment_id)
			       security.team_id, security.fragment_id, security.decision
			FROM evidence_security_events security
			WHERE security.team_id = ?::uuid
			ORDER BY security.team_id, security.fragment_id, security.created_at DESC, security.security_event_id DESC
		)
		SELECT fragment.content_hash, fragment.space_id::text, fragment.space_generation,
		       COALESCE(fragment.source_id::text, ''), COALESCE(fragment.source_revision_id::text, ''),
		       CASE
		           WHEN fragment.source_id IS NOT NULL THEN 'source:' || fragment.source_id::text
		           WHEN btrim(fragment.source_ref) <> '' THEN 'owner:' || fragment.owner_profile_id::text || ':' || fragment.source_ref
		           ELSE 'ingest:' || fragment.ingest_id::text
		       END,
		       fragment.authority
		FROM evidence_fragments fragment
		JOIN teams team
		  ON team.id = fragment.team_id
		JOIN knowledge_ingests ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		LEFT JOIN evidence_sources source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		 AND source.space_id = fragment.space_id
		 AND source.space_generation = fragment.space_generation
		JOIN search_documents document
		  ON document.team_id = fragment.team_id
		 AND document.source_kind = 'evidence'
		 AND document.source_id = fragment.fragment_id
		 AND document.source_version = 1
		 AND document.search_state = 'current'
		 AND document.embedding IS NOT NULL
		 AND document.document_hash = regexp_replace(fragment.content_hash, '^sha256:', '')
		WHERE fragment.team_id = ?::uuid
		  AND team.status = 'active'
		  AND team.deleted_at IS NULL
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.space_id = dense_mem_team_shared_space(fragment.team_id)
		  AND fragment.space_generation = dense_mem_team_shared_generation(fragment.team_id)
		  AND fragment.content_hash = ?
		  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_exact_aliases alias
		      WHERE alias.team_id = fragment.team_id AND alias.alias_fragment_id = fragment.fragment_id
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM evidence_quarantines quarantine
		      WHERE quarantine.team_id = fragment.team_id AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.space_id = fragment.space_id AND quarantine.space_generation = fragment.space_generation
		        AND quarantine.status = 'active'
		  )
			  AND NOT EXISTS (
			      SELECT 1 FROM evidence_lifecycle_events lifecycle
			      WHERE lifecycle.team_id = fragment.team_id AND lifecycle.target_fragment_id = fragment.fragment_id
			        AND lifecycle.space_id = fragment.space_id AND lifecycle.space_generation = fragment.space_generation
			  )
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
			  AND EXISTS (
		      SELECT 1
		      FROM latest_security security
		      WHERE security.team_id = fragment.team_id
		        AND security.fragment_id = fragment.fragment_id
		        AND security.decision IN ('pass', 'released')
		  )
		`, teamID, teamID, target.EvidenceID, target.ContentHash).Row().Scan(
		&contentHash, &spaceID, &spaceGeneration, &sourceID, &sourceRevisionID, &sourceGroupKey, &authority,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDreamSourceStale
	}
	if err != nil {
		return err
	}
	if contentHash != target.ContentHash || spaceID != target.SpaceID || spaceGeneration != target.SpaceGeneration ||
		sourceID != target.SourceID || sourceRevisionID != target.SourceRevisionID ||
		sourceGroupKey != target.SourceGroupKey || authority != target.Authority {
		return ErrDreamSourceStale
	}
	return nil
}

func validateEvidenceDiscoveryProposalInTx(ctx context.Context, tx *gorm.DB, teamID, targetEvidenceID string, input UpsertHypothesisInput) error {
	if len(input.EvidenceDerivations) == 0 {
		return errors.New("evidence derivations are required")
	}
	targetCited := false
	seenEvidenceSpans := map[string]struct{}{}
	for index, derivation := range input.EvidenceDerivations {
		if derivation.EvidenceID == targetEvidenceID {
			targetCited = true
		}
		spanKey := fmt.Sprintf("%s:%d:%d", derivation.EvidenceID, derivation.SpanStart, derivation.SpanEnd)
		if _, exists := seenEvidenceSpans[spanKey]; exists {
			return fmt.Errorf("evidence_derivations[%d] duplicates a cited span", index)
		}
		seenEvidenceSpans[spanKey] = struct{}{}
		var content string
		var sourceID, sourceRevisionID, sourceGroupKey, authority string
		err := tx.WithContext(ctx).Raw(`
			WITH latest_security AS (
				SELECT DISTINCT ON (security.team_id, security.fragment_id)
				       security.team_id, security.fragment_id, security.decision
				FROM evidence_security_events security
				WHERE security.team_id = ?::uuid
				ORDER BY security.team_id, security.fragment_id, security.created_at DESC, security.security_event_id DESC
			)
			SELECT fragment.content, COALESCE(fragment.source_id::text, ''),
			       COALESCE(fragment.source_revision_id::text, ''),
			       CASE
			           WHEN fragment.source_id IS NOT NULL THEN 'source:' || fragment.source_id::text
			           WHEN btrim(fragment.source_ref) <> '' THEN 'owner:' || fragment.owner_profile_id::text || ':' || fragment.source_ref
			           ELSE 'ingest:' || fragment.ingest_id::text
			       END,
			       fragment.authority
		FROM evidence_fragments fragment
		JOIN knowledge_ingests ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
			LEFT JOIN evidence_sources source
			  ON source.team_id = fragment.team_id AND source.source_id = fragment.source_id
			 AND source.space_id = fragment.space_id AND source.space_generation = fragment.space_generation
			WHERE fragment.team_id = ?::uuid AND fragment.fragment_id = ?::uuid
			  AND fragment.space_id = dense_mem_team_shared_space(fragment.team_id)
			  AND fragment.space_generation = dense_mem_team_shared_generation(fragment.team_id)
			  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND COALESCE(ingest.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND NOT EXISTS (SELECT 1 FROM evidence_exact_aliases alias WHERE alias.team_id = fragment.team_id AND alias.alias_fragment_id = fragment.fragment_id)
			  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines quarantine WHERE quarantine.team_id = fragment.team_id AND quarantine.fragment_id = fragment.fragment_id AND quarantine.status = 'active')
			  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events lifecycle WHERE lifecycle.team_id = fragment.team_id AND lifecycle.target_fragment_id = fragment.fragment_id)
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
			  AND EXISTS (
			      SELECT 1
			      FROM latest_security security
			      WHERE security.team_id = fragment.team_id
			        AND security.fragment_id = fragment.fragment_id
			        AND security.decision IN ('pass', 'released')
			  )
		`, teamID, teamID, derivation.EvidenceID).Row().Scan(&content, &sourceID, &sourceRevisionID, &sourceGroupKey, &authority)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("evidence_derivations[%d] cites ineligible evidence", index)
		}
		if err != nil {
			return err
		}
		if sourceID != derivation.SourceID || sourceRevisionID != derivation.SourceRevisionID || sourceGroupKey != derivation.SourceGroupKey || authority != derivation.Authority {
			return fmt.Errorf("evidence_derivations[%d] metadata does not match current evidence", index)
		}
		runes := []rune(content)
		if derivation.SpanStart < 0 || derivation.SpanEnd > len(runes) || derivation.SpanEnd <= derivation.SpanStart || string(runes[derivation.SpanStart:derivation.SpanEnd]) != derivation.Quote {
			return fmt.Errorf("evidence_derivations[%d] span does not match evidence", index)
		}
	}
	if !targetCited {
		return errors.New("evidence derivations must cite the target evidence")
	}
	return nil
}
