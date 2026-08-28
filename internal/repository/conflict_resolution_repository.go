package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *LedgerRepositoryImpl) PlanRelationshipConflictResolution(
	ctx context.Context,
	input RelationshipConflictResolutionInput,
) (*RelationshipConflictResolutionPlan, error) {
	input = normalizeRelationshipConflictResolutionInput(input)
	if err := validateRelationshipConflictResolutionInput(input); err != nil {
		return nil, err
	}
	plan := &RelationshipConflictResolutionPlan{Resolution: input, Documents: []RelationshipConflictResolutionDocument{}}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		loaded, err := buildRelationshipConflictResolutionPlan(ctx, tx, input)
		if err != nil {
			return err
		}
		*plan = *loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: plan resolution: %w", err)
	}
	return plan, nil
}

func (r *LedgerRepositoryImpl) CommitRelationshipConflictResolution(
	ctx context.Context,
	input CommitRelationshipConflictResolutionInput,
) (*ApplyOverdueConflictResolutionResult, error) {
	input.Plan.Resolution = normalizeRelationshipConflictResolutionInput(input.Plan.Resolution)
	if err := validateRelationshipConflictResolutionInput(input.Plan.Resolution); err != nil {
		return nil, err
	}
	result := &ApplyOverdueConflictResolutionResult{
		ConflictID:          input.Plan.Resolution.ConflictID,
		PreferredPositionID: input.Plan.Resolution.PreferredPositionID,
		Method:              input.Plan.Resolution.Method,
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		resolution := input.Plan.Resolution
		if err := setConflictSystemTeamContext(ctx, tx, resolution.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, resolution.TeamID); err != nil {
			return err
		}
		current, err := buildRelationshipConflictResolutionPlan(ctx, tx, resolution)
		if err != nil {
			return err
		}
		if current.Stale {
			result.Stale = true
			return nil
		}
		if current.Pending {
			result.Pending = true
			return nil
		}
		if !relationshipConflictResolutionPlansEqual(input.Plan, *current) {
			result.Stale = true
			return nil
		}
		vectors, err := conflictResolutionEmbeddingsByHash(current.Documents, input.Embeddings)
		if err != nil {
			return err
		}
		for _, document := range current.Documents {
			if err := applyConflictResolutionEmbedding(ctx, tx, document, current.Fence, vectors[document.DocumentHash]); err != nil {
				return err
			}
		}

		records, err := loadRelationshipConflictRecordsByID(ctx, tx, resolution.TeamID, []string{resolution.ConflictID}, nil)
		if err != nil {
			return err
		}
		if len(records) != 1 || records[0].Version != resolution.ExpectedCaseVersion {
			result.Stale = true
			return nil
		}
		evaluation := RelationshipConflictEvaluation{
			Outcome:             ConflictReviewOutcomeResolve,
			Stage:               "resolution_" + resolution.Method,
			PreferredPositionID: resolution.PreferredPositionID,
			Reason:              current.Reason,
			EffectiveAt:         &current.EffectiveAt,
			EffectiveTimeBasis:  current.EffectiveTimeBasis,
		}
		updated, err := resolveRelationshipConflictCase(ctx, tx, ReviewRelationshipConflictCaseInput{
			TeamID: resolution.TeamID, ConflictID: resolution.ConflictID, ReviewRunID: resolution.ReviewRunID,
			WorkerID: resolution.WorkerID, Now: resolution.Now,
		}, &records[0], evaluation)
		if err != nil {
			return err
		}
		result.UpdatedRelationships = updated

		if resolution.Method == "deterministic" {
			result.Resolved = true
			return nil
		}
		targets, err := loadConflictLosingEvidenceTargets(ctx, tx, resolution.TeamID, resolution.ConflictID, resolution.PreferredPositionID)
		if err != nil {
			return err
		}
		systemProfileID, err := ensureConflictSystemProfile(ctx, tx, resolution.TeamID)
		if err != nil {
			return err
		}
		legacyInput := ApplyOverdueConflictResolutionInput(resolution)
		retracted, err := retractConflictLosingEvidence(ctx, tx, legacyInput, systemProfileID, targets)
		if err != nil {
			return err
		}
		derived, err := enqueueConflictDerivedEvidenceTasks(ctx, tx, current.ResolutionPlanID, conflictDerivedEvidenceTargets(
			resolution.TeamID, resolution.ConflictID, systemProfileID, resolution.PreferredPositionID, targets, retracted,
		))
		if err != nil {
			return err
		}
		update := tx.WithContext(ctx).Exec(`
			UPDATE relationship_conflict_resolution_plans
			SET status = 'applied', applied_at = now(), failure_reason = ''
			WHERE team_id = ?::uuid
			  AND resolution_plan_id = ?::uuid
			  AND status = 'resolution_pending'
		`, resolution.TeamID, current.ResolutionPlanID)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrConflictAssessmentStale
		}
		result.Resolved = true
		result.RetractedEvidenceIDs = retracted
		result.DerivedEvidence = derived
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: commit resolution: %w", err)
	}
	return result, nil
}

func buildRelationshipConflictResolutionPlan(
	ctx context.Context,
	tx *gorm.DB,
	input RelationshipConflictResolutionInput,
) (*RelationshipConflictResolutionPlan, error) {
	plan := &RelationshipConflictResolutionPlan{Resolution: input, Documents: []RelationshipConflictResolutionDocument{}}
	reviewInput := ReviewRelationshipConflictCaseInput{
		TeamID: input.TeamID, ConflictID: input.ConflictID, ReviewRunID: input.ReviewRunID,
		WorkerID: input.WorkerID, Now: input.Now,
	}
	record, err := loadRelationshipConflictCaseForResolution(ctx, tx, input, reviewInput)
	if err != nil {
		if errors.Is(err, ErrPlacementLeaseLost) || errors.Is(err, ErrConflictAssessmentStale) {
			plan.Stale = true
			return plan, nil
		}
		return nil, err
	}
	if err := validateConflictResolutionPosition(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID); err != nil {
		if errors.Is(err, ErrConflictAssessmentStale) {
			plan.Stale = true
			return plan, nil
		}
		return nil, err
	}
	if err := validateConflictResolutionSpaceFence(ctx, tx, input); err != nil {
		if errors.Is(err, ErrConflictAssessmentStale) {
			plan.Stale = true
			return plan, nil
		}
		return nil, err
	}
	record, dismissed, err := refreshRelationshipConflictCaseSnapshotForReview(ctx, tx, reviewInput, record)
	if err != nil {
		return nil, err
	}
	if dismissed || record.Version != input.ExpectedCaseVersion {
		plan.Stale = true
		return plan, nil
	}
	effectiveAt, effectiveBasis := conflictResolutionEffectiveTime(*record, input.PreferredPositionID, input.Now)
	plan.EffectiveAt = effectiveAt
	plan.EffectiveTimeBasis = effectiveBasis
	switch input.Method {
	case "deterministic":
		if record.Status != string(domain.RelationshipConflictOpen) {
			plan.Stale = true
			return plan, nil
		}
		evaluation := EvaluateRelationshipConflict(RelationshipConflictEvaluationInput{
			Now: input.Now, ReviewDueAt: record.ReviewDueAt, Positions: record.Positions,
		})
		if evaluation.Outcome != ConflictReviewOutcomeResolve || evaluation.PreferredPositionID != input.PreferredPositionID {
			plan.Stale = true
			return plan, nil
		}
		plan.Reason = evaluation.Reason
		if evaluation.EffectiveAt != nil {
			plan.EffectiveAt = evaluation.EffectiveAt.UTC()
		}
		if evaluation.EffectiveTimeBasis != "" {
			plan.EffectiveTimeBasis = evaluation.EffectiveTimeBasis
		}
	case "ai", "last_write_wins":
		if record.Status != string(domain.RelationshipConflictOverdue) {
			plan.Stale = true
			return plan, nil
		}
		legacyInput := ApplyOverdueConflictResolutionInput(input)
		if err := validateConflictResolutionAssessment(ctx, tx, legacyInput); err != nil {
			if errors.Is(err, ErrConflictAssessmentStale) {
				plan.Stale = true
				return plan, nil
			}
			return nil, err
		}
		planID, status, err := ensureConflictResolutionPlan(ctx, tx, legacyInput, plan.EffectiveAt, plan.EffectiveTimeBasis)
		if err != nil {
			return nil, err
		}
		plan.ResolutionPlanID = planID
		if status == "applied" {
			plan.Stale = true
			return plan, nil
		}
		if status == "superseded" || status == "failed" {
			plan.Stale = true
			return plan, nil
		}
		targets, err := loadConflictLosingEvidenceTargets(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID)
		if err != nil {
			return nil, err
		}
		if len(targets) > conflictResolutionMaxFragments {
			pendingKey := "case:" + input.ConflictID + ":plan:" + planID + ":pending"
			var exists bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT EXISTS (
					SELECT 1 FROM relationship_conflict_events
					WHERE team_id = ?::uuid AND idempotency_key = ?
				)
			`, input.TeamID, pendingKey).Row().Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, input.PreferredPositionID, "", "", string(domain.RelationshipConflictEventResolutionPending), "fanout_bound", pendingKey, map[string]any{
					"resolution_plan_id": planID, "target_fragment_count": len(targets),
				}); err != nil {
					return nil, err
				}
				plan.PendingTransitioned = true
			}
			plan.Pending = true
			return plan, nil
		}
		plan.Reason = conflictResolutionReason(input.Method)
	default:
		return nil, errors.New("conflict resolution method is unsupported")
	}

	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	plan.Fence = RelationshipConflictResolutionFence{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		EmbeddingModel: contract.EmbeddingModel, SearchIndexGenerationID: contract.SearchIndexGenerationID,
		IndexGeneration: contract.IndexGeneration,
	}
	documents, err := loadConflictResolutionDocuments(ctx, tx, input, contract)
	if err != nil {
		return nil, err
	}
	plan.Documents = documents
	return plan, nil
}

func validateConflictResolutionSpaceFence(ctx context.Context, tx *gorm.DB, input RelationshipConflictResolutionInput) error {
	var valid bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM relationship_conflict_cases AS conflict
			JOIN memory_spaces AS conflict_space
			  ON conflict_space.team_id = conflict.team_id
			 AND conflict_space.id = conflict.space_id
			 AND conflict_space.generation = conflict.space_generation
			 AND conflict_space.lifecycle_state = 'active'
			JOIN relationship_conflict_position_members AS member
			  ON member.team_id = conflict.team_id
			 AND member.conflict_id = conflict.conflict_id
			 AND member.position_id = ?::uuid
			 AND member.active
			JOIN relationship_records AS relationship
			  ON relationship.team_id = member.team_id
			 AND relationship.relationship_id = member.relationship_id
			 AND relationship.status = 'active'
			 AND relationship.support_count > 0
			JOIN memory_spaces AS relationship_space
			  ON relationship_space.team_id = relationship.team_id
			 AND relationship_space.id = relationship.space_id
			 AND relationship_space.generation = relationship.space_generation
			 AND relationship_space.lifecycle_state = 'active'
			WHERE conflict.team_id = ?::uuid
			  AND conflict.conflict_id = ?::uuid
			  AND conflict.version = ?
			FOR KEY SHARE OF conflict, conflict_space, relationship, relationship_space
		)
	`, input.PreferredPositionID, input.TeamID, input.ConflictID, input.ExpectedCaseVersion).Row().Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflictAssessmentStale
	}
	return nil
}

func loadRelationshipConflictCaseForResolution(
	ctx context.Context,
	tx *gorm.DB,
	resolution RelationshipConflictResolutionInput,
	review ReviewRelationshipConflictCaseInput,
) (*RelationshipConflictCaseRecord, error) {
	if resolution.Method == "deterministic" {
		return loadRelationshipConflictCaseForReview(ctx, tx, review)
	}
	var conflictID string
	err := tx.WithContext(ctx).Raw(`
		SELECT conflict_id::text
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND status = 'overdue'
		  AND version = ?
		FOR UPDATE
	`, resolution.TeamID, resolution.ConflictID, resolution.ExpectedCaseVersion).Row().Scan(&conflictID)
	if errors.Is(err, sql.ErrNoRows) || conflictID == "" {
		return nil, ErrConflictAssessmentStale
	}
	if err != nil {
		return nil, err
	}
	records, err := loadRelationshipConflictRecordsByID(ctx, tx, resolution.TeamID, []string{resolution.ConflictID}, nil)
	if err != nil {
		return nil, err
	}
	if len(records) != 1 {
		return nil, sql.ErrNoRows
	}
	return &records[0], nil
}

func loadConflictResolutionDocuments(
	ctx context.Context,
	tx *gorm.DB,
	input RelationshipConflictResolutionInput,
	contract *ActiveSearchContract,
) ([]RelationshipConflictResolutionDocument, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT relationship.relationship_id::text
		FROM relationship_conflict_position_members AS member
		JOIN relationship_records AS relationship
		  ON relationship.team_id = member.team_id
		 AND relationship.relationship_id = member.relationship_id
		JOIN memory_spaces AS space
		  ON space.team_id = relationship.team_id
		 AND space.id = relationship.space_id
		 AND space.generation = relationship.space_generation
		 AND space.lifecycle_state = 'active'
		WHERE member.team_id = ?::uuid
		  AND member.conflict_id = ?::uuid
		  AND member.position_id = ?::uuid
		  AND member.active
		  AND relationship.status = 'active'
		  AND relationship.support_count > 0
		ORDER BY relationship.relationship_id::text
	`, input.TeamID, input.ConflictID, input.PreferredPositionID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	relationshipIDs := []string{}
	for rows.Next() {
		var relationshipID string
		if err := rows.Scan(&relationshipID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	assignments := []RelationshipConflictResolutionDocument{}
	seenHashes := make(map[string]struct{})
	for _, relationshipID := range relationshipIDs {
		relationship, err := loadRelationshipRecord(ctx, tx, input.TeamID, relationshipID)
		if err != nil {
			return nil, err
		}
		text, err := placementRelationshipSearchText(ctx, tx, relationship)
		if err != nil {
			return nil, err
		}
		normalized := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{DocumentText: text})
		required, err := conflictResolutionEmbeddingRequired(ctx, tx, relationship, contract, normalized.DocumentHash)
		if err != nil {
			return nil, err
		}
		if !required {
			continue
		}
		assignments = append(assignments, RelationshipConflictResolutionDocument{
			TeamID: relationship.TeamID, RelationshipID: relationship.RelationshipID, OwnerProfileID: relationship.OwnerProfileID,
			SpaceID: relationship.SpaceID, SpaceGeneration: relationship.SpaceGeneration,
			SourceVersion: int64(relationship.Version), DocumentHash: normalized.DocumentHash, DocumentText: normalized.DocumentText,
		})
		seenHashes[normalized.DocumentHash] = struct{}{}
		if len(seenHashes) > 256 {
			return nil, errors.New("conflict resolution requires more than 256 unique search documents")
		}
	}
	return assignments, nil
}

func conflictResolutionEmbeddingRequired(
	ctx context.Context,
	tx *gorm.DB,
	relationship *RelationshipRecord,
	contract *ActiveSearchContract,
	documentHash string,
) (bool, error) {
	var state, hash, spaceID string
	var dimensions int
	var spaceGeneration, sourceVersion int64
	var hasEmbedding bool
	err := tx.WithContext(ctx).Raw(`
		SELECT search_state, document_hash, embedding_dimensions, space_id::text, space_generation,
		       source_version, embedding IS NOT NULL
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND source_kind = 'relationship'
		  AND source_id = ?::uuid
		  AND embedding_contract_id = ?::uuid
		LIMIT 1
	`, relationship.TeamID, relationship.RelationshipID, contract.EmbeddingContractID).Row().Scan(
		&state, &hash, &dimensions, &spaceID, &spaceGeneration, &sourceVersion, &hasEmbedding,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return state != string(domain.SearchProjectionCurrent) || hash != documentHash ||
		dimensions != contract.EmbeddingDimensions || spaceID != relationship.SpaceID ||
		spaceGeneration != relationship.SpaceGeneration || sourceVersion != int64(relationship.Version) || !hasEmbedding, nil
}

func conflictResolutionEmbeddingsByHash(
	documents []RelationshipConflictResolutionDocument,
	provided []RelationshipConflictResolutionEmbedding,
) (map[string][]float32, error) {
	required := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		required[document.DocumentHash] = struct{}{}
	}
	if len(required) == 0 && len(provided) == 0 {
		return map[string][]float32{}, nil
	}
	values := make(map[string][]float32, len(provided))
	for _, embedding := range provided {
		hash := strings.TrimSpace(embedding.DocumentHash)
		if _, ok := required[hash]; !ok || len(embedding.Embedding) == 0 {
			return nil, errors.New("conflict resolution embeddings do not match the plan")
		}
		if _, exists := values[hash]; exists {
			return nil, errors.New("conflict resolution embeddings contain duplicate document hashes")
		}
		values[hash] = append([]float32(nil), embedding.Embedding...)
	}
	if len(values) != len(required) {
		return nil, errors.New("conflict resolution embeddings are incomplete")
	}
	return values, nil
}

func applyConflictResolutionEmbedding(
	ctx context.Context,
	tx *gorm.DB,
	document RelationshipConflictResolutionDocument,
	fence RelationshipConflictResolutionFence,
	vector []float32,
) error {
	if len(vector) != fence.EmbeddingDimensions {
		return errors.New("conflict resolution embedding dimensions do not match the plan")
	}
	previousGenerationID, err := relationshipSearchDocumentProjectionGenerationID(
		ctx, tx, document.TeamID, document.RelationshipID, fence.EmbeddingContractID,
	)
	if err != nil {
		return err
	}
	vectorValue, err := vectorLiteral(vector)
	if err != nil {
		return err
	}
	metadata, err := relationshipForegroundSearchMetadata(ctx, tx, document.TeamID)
	if err != nil {
		return err
	}
	metadataJSON, err := marshalSearchJSON(metadata)
	if err != nil {
		return err
	}
	var searchDocumentID string
	err = tx.WithContext(ctx).Raw(`
		WITH upserted AS (
			INSERT INTO search_documents (
			    team_id, owner_profile_id, space_id, space_generation, source_kind, source_id, source_version,
			    projection_format_version, projection_generation_id, document_version, embedding_contract_id,
			    embedding_dimensions, search_state, document_text, document_hash, embedding, embedding_updated_at, embedding_error, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?, 'relationship', ?::uuid, ?, 2, NULL, 1, ?::uuid,
			    ?, 'current', ?, ?, ?::vector, now(), '', ?::jsonb
			)
			ON CONFLICT (team_id, source_kind, source_id, embedding_contract_id)
			DO UPDATE SET
			    owner_profile_id = EXCLUDED.owner_profile_id,
			    source_version = EXCLUDED.source_version,
			    projection_format_version = EXCLUDED.projection_format_version,
			    projection_generation_id = NULL,
			    document_version = CASE
			        WHEN search_documents.document_hash = EXCLUDED.document_hash
			         AND search_documents.projection_format_version = EXCLUDED.projection_format_version
			         AND search_documents.projection_generation_id IS NULL
			        THEN search_documents.document_version
			        ELSE search_documents.document_version + 1
			    END,
			    search_state = 'current', document_text = EXCLUDED.document_text, document_hash = EXCLUDED.document_hash,
			    embedding = EXCLUDED.embedding, embedding_updated_at = now(), embedding_error = '', metadata = EXCLUDED.metadata,
			    updated_at = now()
			WHERE EXCLUDED.source_version >= search_documents.source_version
			  AND search_documents.space_id = EXCLUDED.space_id
			  AND search_documents.space_generation = EXCLUDED.space_generation
			RETURNING search_document_id::text
		)
		SELECT search_document_id FROM upserted
	`, document.TeamID, document.OwnerProfileID, document.SpaceID, document.SpaceGeneration,
		document.RelationshipID, document.SourceVersion, fence.EmbeddingContractID, fence.EmbeddingDimensions,
		document.DocumentText, document.DocumentHash, vectorValue, string(metadataJSON)).Row().Scan(&searchDocumentID)
	if errors.Is(err, sql.ErrNoRows) || searchDocumentID == "" {
		return ErrConflictAssessmentStale
	}
	if err != nil {
		return err
	}
	if err := staleConflictRelationshipEmbeddingJobs(ctx, tx, document.TeamID, []string{document.RelationshipID}); err != nil {
		return err
	}
	return refreshPreviousRelationshipProjectionGeneration(ctx, tx, document.TeamID, previousGenerationID)
}

func staleConflictRelationshipEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string, relationshipIDs []string) error {
	if len(relationshipIDs) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs
		SET status = 'stale', error = 'conflict resolution superseded embedding work', completed_at = now(),
		    lease_until = NULL, worker_id = '', updated_at = now()
		WHERE team_id = ?::uuid
		  AND source_kind = 'relationship'
		  AND source_id = ANY(?::uuid[])
		  AND status IN ('queued', 'processing', 'failed')
	`, teamID, pq.Array(relationshipIDs)).Error
}

func relationshipConflictResolutionPlansEqual(left, right RelationshipConflictResolutionPlan) bool {
	if left.Resolution != right.Resolution || left.Fence != right.Fence ||
		!left.EffectiveAt.Equal(right.EffectiveAt) || left.EffectiveTimeBasis != right.EffectiveTimeBasis ||
		left.Reason != right.Reason || left.ResolutionPlanID != right.ResolutionPlanID ||
		left.Pending != right.Pending || left.PendingTransitioned != right.PendingTransitioned || left.Stale != right.Stale || len(left.Documents) != len(right.Documents) {
		return false
	}
	for index := range left.Documents {
		if left.Documents[index] != right.Documents[index] {
			return false
		}
	}
	return true
}

func normalizeRelationshipConflictResolutionInput(input RelationshipConflictResolutionInput) RelationshipConflictResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.PreferredPositionID = strings.TrimSpace(input.PreferredPositionID)
	input.AssessmentAttemptID = strings.TrimSpace(input.AssessmentAttemptID)
	input.Method = strings.TrimSpace(input.Method)
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	return input
}

func validateRelationshipConflictResolutionInput(input RelationshipConflictResolutionInput) error {
	for _, value := range []struct{ name, id string }{
		{name: "team_id", id: input.TeamID}, {name: "conflict_id", id: input.ConflictID},
		{name: "review_run_id", id: input.ReviewRunID}, {name: "preferred_position_id", id: input.PreferredPositionID},
	} {
		if _, err := uuid.Parse(value.id); err != nil {
			return fmt.Errorf("%s is required: %w", value.name, err)
		}
	}
	if input.WorkerID == "" || input.ExpectedCaseVersion < 1 {
		return errors.New("conflict resolution requires a worker and expected case version")
	}
	switch input.Method {
	case "deterministic":
		if input.AssessmentAttemptID != "" {
			return errors.New("deterministic conflict resolution must not include an assessment")
		}
	case "ai", "last_write_wins":
		if _, err := uuid.Parse(input.AssessmentAttemptID); err != nil {
			return fmt.Errorf("assessment_attempt_id is required: %w", err)
		}
	default:
		return errors.New("conflict resolution method is unsupported")
	}
	return nil
}
