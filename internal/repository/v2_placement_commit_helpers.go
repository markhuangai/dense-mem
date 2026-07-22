package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const v2PlacementRunGuardedStatusCase = `
	CASE
	    WHEN EXISTS (
	        SELECT 1
	        FROM placement_items AS item
	        JOIN evidence_security_events AS event
	          ON event.team_id = item.team_id
	         AND event.fragment_id = item.fragment_id
	         AND event.owner_profile_id = item.owner_profile_id
	        WHERE item.team_id = placement_runs.team_id
	          AND item.placement_run_id = placement_runs.placement_run_id
	          AND item.status IN ('queued', 'processing')
	          AND event.decision = 'guarded'
	    ) THEN 'guarded'
	    ELSE 'queued'
	END`

func appendV2PlacementSearchDocument(result *V2CommitPlacementSemanticResult, document *V2SearchDocumentResult) {
	if result == nil || document == nil || document.SearchDocumentID == "" {
		return
	}
	for _, existing := range result.SearchDocuments {
		if existing.SearchDocumentID == document.SearchDocumentID {
			return
		}
	}
	result.SearchDocuments = append(result.SearchDocuments, *document)
}

type v2PlacementCorrectionTargetRecord struct {
	SubjectEntityID string
	PredicateKey    string
	ObjectEntityID  string
	ObjectValueID   string
}

func appendV2PlacementCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	applied *V2RelationshipDecisionResult,
	target V2PlacementCorrectionTargetInput,
) error {
	if applied == nil || applied.Relationship == nil || applied.VerificationEventID == "" {
		return errors.New("correction target requires an applied source relationship and verification event")
	}
	source := applied.Relationship
	if err := requireV2RelationshipVersion(ctx, tx, commit.TeamID, source.RelationshipID, commit.OwnerProfileID, source.Version); err != nil {
		return err
	}
	if err := requireV2RelationshipVersion(ctx, tx, commit.TeamID, target.RelationshipID, "", target.ExpectedVersion); err != nil {
		return err
	}
	if err := requireV2VerificationForRelationship(ctx, tx, commit.TeamID, applied.VerificationEventID, commit.OwnerProfileID, source.RelationshipID); err != nil {
		return err
	}
	targetRecord, err := loadV2PlacementCorrectionTarget(ctx, tx, commit.TeamID, target)
	if err != nil {
		return err
	}
	if !v2PlacementCorrectionTargetRelated(source, targetRecord) {
		return errors.New("correction target is not semantically related to the source relationship")
	}
	metadata, err := marshalV2JSON(map[string]any{
		"source":           "correction_target",
		"contract_version": domain.V2ContractVersion,
	})
	if err != nil {
		return err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_cross_references (
		    team_id, author_profile_id, source_relationship_id,
		    source_relationship_version, target_relationship_id,
		    target_relationship_version, kind, verification_event_id, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?::uuid, ?::jsonb
		)
		RETURNING cross_reference_id::text
	`, commit.TeamID, commit.OwnerProfileID, source.RelationshipID, source.Version,
		target.RelationshipID, target.ExpectedVersion, string(domain.V2CrossReferenceCorrects),
		applied.VerificationEventID, string(metadata)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	var crossReferenceID string
	if err := rows.Scan(&crossReferenceID); err != nil {
		return err
	}
	return rows.Err()
}

func loadV2PlacementCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	target V2PlacementCorrectionTargetInput,
) (v2PlacementCorrectionTargetRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT subject_entity_id::text,
		       predicate_key,
		       COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, '')
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND version = ?
	`, teamID, target.RelationshipID, target.ExpectedVersion).Rows()
	if err != nil {
		return v2PlacementCorrectionTargetRecord{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return v2PlacementCorrectionTargetRecord{}, err
		}
		return v2PlacementCorrectionTargetRecord{}, sql.ErrNoRows
	}
	var record v2PlacementCorrectionTargetRecord
	if err := rows.Scan(&record.SubjectEntityID, &record.PredicateKey, &record.ObjectEntityID, &record.ObjectValueID); err != nil {
		return v2PlacementCorrectionTargetRecord{}, err
	}
	return record, rows.Err()
}

func v2PlacementCorrectionTargetRelated(source *V2RelationshipRecord, target v2PlacementCorrectionTargetRecord) bool {
	if source == nil || source.PredicateKey == "" || source.PredicateKey != target.PredicateKey {
		return false
	}
	if source.SubjectEntityID != "" && source.SubjectEntityID == target.SubjectEntityID {
		return true
	}
	if source.ObjectEntityID != "" && source.ObjectEntityID == target.ObjectEntityID {
		return true
	}
	return source.ObjectValueID != "" && source.ObjectValueID == target.ObjectValueID
}

func applyV2RelationshipDecisionInTx(
	ctx context.Context,
	tx *gorm.DB,
	input V2ApplyRelationshipDecisionInput,
) (*V2RelationshipDecisionResult, error) {
	input = normalizeV2ApplyRelationshipDecisionInput(input)
	predicate, err := loadV2PredicateDefinition(ctx, tx, input.PredicateKey, input.PredicateVersion)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return insertV2PredicateReview(ctx, tx, input)
	}
	if err != nil {
		return nil, err
	}
	if err := validateV2RelationshipEndpointKinds(ctx, tx, input, predicate); err != nil {
		return nil, err
	}
	tier, status := v2TierStatusForVerdict(input.EvidenceVerdict, input.PromoteToFact)
	groupKey := v2SemanticGroupKey(input)
	recordState, err := upsertV2RelationshipRecord(ctx, tx, input, predicate, tier, status, groupKey)
	if err != nil {
		return nil, err
	}
	observationID, err := insertV2RelationshipObservation(ctx, tx, input, recordState.Record.RelationshipID)
	if err != nil {
		return nil, err
	}
	verificationID, err := insertV2VerificationEvent(ctx, tx, input, observationID)
	if err != nil {
		return nil, err
	}
	var supportID, supportDecisionID string
	if input.EvidenceVerdict == string(domain.V2VerificationEntailed) && input.Support != nil {
		supportID, supportDecisionID, err = insertV2RelationshipSupport(ctx, tx, input, recordState.Record.RelationshipID, observationID, verificationID)
		if err != nil {
			return nil, err
		}
		if err := refreshV2RelationshipSupportCounts(ctx, tx, input.TeamID, recordState.Record.RelationshipID); err != nil {
			return nil, err
		}
	}
	if recordState.Changed {
		if _, err := insertV2RelationshipTransition(ctx, tx, v2TransitionInput{
			TeamID:              input.TeamID,
			OwnerProfileID:      input.OwnerProfileID,
			RelationshipID:      recordState.Record.RelationshipID,
			FromTier:            recordState.FromTier,
			FromStatus:          recordState.FromStatus,
			ToTier:              tier,
			ToStatus:            status,
			Reason:              "verifier_decision",
			VerificationEventID: verificationID,
			SupportDecisionID:   supportDecisionID,
			IdempotencyKey:      v2RelationshipTransitionIdempotencyKey(verificationID, supportDecisionID),
		}); err != nil {
			return nil, err
		}
	}
	loaded, err := loadV2RelationshipRecord(ctx, tx, input.TeamID, recordState.Record.RelationshipID)
	if err != nil {
		return nil, err
	}
	return &V2RelationshipDecisionResult{
		Relationship:        loaded,
		ObservationID:       observationID,
		VerificationEventID: verificationID,
		SupportID:           supportID,
		SupportDecisionID:   supportDecisionID,
		CreatedRelationship: recordState.Created,
	}, nil
}

func withV2PlacementDecisionScope(input V2CommitPlacementSemanticInput, decision V2ApplyRelationshipDecisionInput) V2ApplyRelationshipDecisionInput {
	decision.TeamID = input.TeamID
	decision.OwnerProfileID = input.OwnerProfileID
	decision.IngestID = input.IngestID
	decision.PlacementItemID = input.PlacementItemID
	return decision
}

func upsertV2PlacementRelationshipSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	relationship *V2RelationshipRecord,
) (*V2SearchDocumentResult, error) {
	contract, err := loadV2ActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	input := normalizeV2UpsertSearchDocumentInput(V2UpsertSearchDocumentInput{
		TeamID:         commit.TeamID,
		OwnerProfileID: commit.OwnerProfileID,
		SourceKind:     "relationship",
		SourceID:       relationship.RelationshipID,
		SourceVersion:  int64(relationship.Version),
		DocumentText:   v2PlacementRelationshipSearchText(relationship),
	})
	if err := validateV2UpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	return upsertV2SearchDocumentInTx(ctx, tx, input, contract)
}

func v2PlacementCommitPayload(base map[string]any, result *V2CommitPlacementSemanticResult) map[string]any {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
	}
	for key, value := range base {
		payload[key] = value
	}
	relationships := make([]string, 0, len(result.RelationshipResults))
	for _, item := range result.RelationshipResults {
		if item.Relationship != nil {
			relationships = append(relationships, item.Relationship.RelationshipID)
		}
	}
	searchDocuments := make([]string, 0, len(result.SearchDocuments))
	embeddingJobs := make([]string, 0, len(result.SearchDocuments))
	for _, item := range result.SearchDocuments {
		searchDocuments = append(searchDocuments, item.SearchDocumentID)
		if item.QueuedJobID != "" {
			embeddingJobs = append(embeddingJobs, item.QueuedJobID)
		}
	}
	payload["relationship_ids"] = relationships
	payload["relationship_outcomes"] = v2PlacementRelationshipOutcomePayload(result.RelationshipResults)
	payload["search_document_ids"] = searchDocuments
	payload["embedding_job_ids"] = embeddingJobs
	payload["entity_resolution_ids"] = append([]string(nil), result.EntityResolutionIDs...)
	return payload
}

func v2RelationshipOutcomeCategory(result *V2RelationshipDecisionResult) string {
	if result == nil {
		return string(domain.V2OutcomeRelationshipRejected)
	}
	if result.ReviewTaskID != "" && result.Relationship == nil {
		return string(domain.V2OutcomePredicateNeedsReview)
	}
	if result.Relationship == nil {
		return string(domain.V2OutcomeRelationshipRejected)
	}
	switch result.Relationship.Status {
	case string(domain.V2RelationshipStatusPendingEvidence):
		return string(domain.V2OutcomeRelationshipPendingEvidence)
	case string(domain.V2RelationshipStatusRejected):
		return string(domain.V2OutcomeRelationshipRejected)
	}
	if result.Relationship.Tier == string(domain.V2RelationshipTierFact) {
		return string(domain.V2OutcomeRelationshipFact)
	}
	return string(domain.V2OutcomeRelationshipValidatedClaim)
}

func v2RelationshipOutcomeReason(decision V2ApplyRelationshipDecisionInput, result *V2RelationshipDecisionResult) string {
	if rationale := strings.TrimSpace(decision.Rationale); rationale != "" {
		return rationale
	}
	if result != nil && result.ReviewTaskID != "" && result.Relationship == nil {
		return "predicate requires review before a canonical relationship can be created"
	}
	if result == nil || result.Relationship == nil {
		return "relationship did not produce an active semantic record"
	}
	switch result.Relationship.Status {
	case string(domain.V2RelationshipStatusPendingEvidence):
		return "evidence was insufficient for active knowledge"
	case string(domain.V2RelationshipStatusRejected):
		return "evidence contradicted the proposed relationship"
	default:
		return "relationship accepted from verified evidence"
	}
}

func v2PlacementRelationshipOutcomePayload(results []V2RelationshipDecisionResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		item := map[string]any{
			"proposal_id":      result.ProposalID,
			"observation_id":   result.ObservationID,
			"owner_profile_id": result.OwnerProfileID,
			"category":         result.Category,
			"reason":           result.Reason,
		}
		if result.Relationship != nil {
			item["relationship_id"] = result.Relationship.RelationshipID
			if result.Relationship.OwnerProfileID != "" {
				item["owner_profile_id"] = result.Relationship.OwnerProfileID
			}
			item["tier"] = result.Relationship.Tier
			item["relationship_status"] = result.Relationship.Status
		}
		if result.ReviewTaskID != "" {
			item["review_task"] = result.ReviewTaskID
		}
		out = append(out, item)
	}
	return out
}

func finishV2PlacementRunIfTerminal(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput, status string) error {
	var openCount int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status IN ('queued', 'processing')
	`, input.TeamID, input.PlacementRunID).Scan(&openCount).Error; err != nil {
		return err
	}
	if openCount > 0 {
		result := tx.WithContext(ctx).Exec(`
			UPDATE placement_runs
			SET status = `+v2PlacementRunGuardedStatusCase+`,
			    worker_id = '',
			    lease_until = NULL,
			    attempts = 0,
			    available_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND status = 'processing'
			  AND worker_id = ?
			  AND attempts = ?
			  AND lease_until > clock_timestamp()
		`, input.TeamID, input.PlacementRunID, input.OwnerProfileID, input.WorkerID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrV2PlacementLeaseLost
		}
		return nil
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = ?,
		    error = '',
		    lease_until = NULL,
		    completed_at = now(),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND status = 'processing'
		  AND worker_id = ?
		  AND attempts = ?
		  AND lease_until > clock_timestamp()
	`, status, input.TeamID, input.PlacementRunID, input.OwnerProfileID, input.WorkerID, input.ExpectedAttempts)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2PlacementLeaseLost
	}
	return nil
}

func requeueV2PlacementRunForRetry(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) error {
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = `+v2PlacementRunGuardedStatusCase+`,
		    worker_id = '',
		    lease_until = NULL,
		    available_at = now(),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND status = 'processing'
		  AND worker_id = ?
		  AND attempts = ?
		  AND lease_until > clock_timestamp()
	`, input.TeamID, input.PlacementRunID, input.OwnerProfileID, input.WorkerID, input.ExpectedAttempts)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrV2PlacementLeaseLost
	}
	return nil
}

func v2IntPointerArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
