package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const placementRunGuardedStatusCase = `
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

func appendPlacementSearchDocument(result *CommitPlacementSemanticResult, document *SearchDocumentResult) {
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

func appendPlacementReviewTaskID(result *CommitPlacementSemanticResult, taskID string) {
	if result == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	for _, existing := range result.ReviewTaskIDs {
		if existing == taskID {
			return
		}
	}
	result.ReviewTaskIDs = append(result.ReviewTaskIDs, taskID)
}

func appendPlacementRelationshipResult(result *CommitPlacementSemanticResult, relationship *RelationshipDecisionResult) {
	if result == nil || relationship == nil {
		return
	}
	result.RelationshipResults = append(result.RelationshipResults, *relationship)
}

func applyPlacementRelationshipDecision(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	decision ApplyRelationshipDecisionInput,
	correctionTarget *PlacementCorrectionTargetInput,
	placementFragmentID string,
	embeddingJobMaxAttempts int,
	conflictConfig ConflictRuntimeConfig,
	result *CommitPlacementSemanticResult,
) error {
	applied, err := applyRelationshipDecisionInTx(ctx, tx, decision)
	if err != nil {
		return err
	}
	applied.ProposalID = decision.ProposalRef
	applied.OwnerProfileID = commit.OwnerProfileID
	applied.Category = relationshipOutcomeCategory(applied)
	applied.Reason = relationshipOutcomeReason(decision, applied)
	result.RelationshipResults = append(result.RelationshipResults, *applied)
	appendPlacementReviewTaskID(result, applied.ReviewTaskID)
	if applied.Relationship == nil || applied.Relationship.Status != string(domain.RelationshipStatusActive) {
		return nil
	}
	if correctionTarget != nil {
		if err := appendPlacementCorrectionTarget(ctx, tx, commit, applied, *correctionTarget); err != nil {
			return err
		}
	}
	if applied.SupportID != "" && decision.Support != nil && decision.Support.FragmentID != "" {
		if placementFragmentID == "" {
			var err error
			placementFragmentID, err = loadPlacementItemFragmentID(ctx, tx, commit)
			if err != nil {
				return err
			}
		}
		if decision.Support.FragmentID != placementFragmentID {
			document, err := upsertPlacementEvidenceSearchDocument(
				ctx,
				tx,
				commit,
				decision.Support.FragmentID,
				map[string]any{
					"supporting_placement_item_id": commit.PlacementItemID,
					"support_id":                   applied.SupportID,
					"relationship_id":              applied.Relationship.RelationshipID,
				},
				embeddingJobMaxAttempts,
			)
			if err != nil {
				return err
			}
			appendPlacementSearchDocument(result, document)
		}
	}
	document, err := upsertPlacementRelationshipSearchDocument(
		ctx,
		tx,
		commit,
		applied.Relationship,
		embeddingJobMaxAttempts,
	)
	if err != nil {
		return err
	}
	appendPlacementSearchDocument(result, document)
	if err := applyRelationshipConflictPlacement(ctx, tx, commit, applied, conflictConfig); err != nil {
		return err
	}
	return nil
}

func placementEvidenceSearchableStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case string(domain.SemanticReviewAccepted), string(domain.SemanticReviewReviewRequired):
		return true
	default:
		return false
	}
}

type placementCorrectionTargetRecord struct {
	SubjectEntityID string
	PredicateKey    string
	ObjectEntityID  string
	ObjectValueID   string
}

func appendPlacementCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	applied *RelationshipDecisionResult,
	target PlacementCorrectionTargetInput,
) error {
	if applied == nil || applied.Relationship == nil || applied.VerificationEventID == "" {
		return errors.New("correction target requires an applied source relationship and verification event")
	}
	source := applied.Relationship
	if err := requireRelationshipVersion(ctx, tx, commit.TeamID, source.RelationshipID, commit.OwnerProfileID, source.Version); err != nil {
		return err
	}
	if err := requireRelationshipVersion(ctx, tx, commit.TeamID, target.RelationshipID, "", target.ExpectedVersion); err != nil {
		return err
	}
	if err := requireVerificationForRelationship(ctx, tx, commit.TeamID, applied.VerificationEventID, commit.OwnerProfileID, source.RelationshipID); err != nil {
		return err
	}
	targetRecord, err := loadPlacementCorrectionTarget(ctx, tx, commit.TeamID, target)
	if err != nil {
		return err
	}
	if !placementCorrectionTargetRelated(source, targetRecord) {
		return errors.New("correction target is not semantically related to the source relationship")
	}
	metadata, err := marshalJSON(map[string]any{
		"source":           "correction_target",
		"contract_version": domain.ContractVersion,
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
		target.RelationshipID, target.ExpectedVersion, string(domain.CrossReferenceCorrects),
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

func loadPlacementCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	target PlacementCorrectionTargetInput,
) (placementCorrectionTargetRecord, error) {
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
		return placementCorrectionTargetRecord{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return placementCorrectionTargetRecord{}, err
		}
		return placementCorrectionTargetRecord{}, sql.ErrNoRows
	}
	var record placementCorrectionTargetRecord
	if err := rows.Scan(&record.SubjectEntityID, &record.PredicateKey, &record.ObjectEntityID, &record.ObjectValueID); err != nil {
		return placementCorrectionTargetRecord{}, err
	}
	return record, rows.Err()
}

func placementCorrectionTargetRelated(source *RelationshipRecord, target placementCorrectionTargetRecord) bool {
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

func insertPlacementEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementEntityResolutionInput,
) (string, error) {
	existingEntityID, err := loadPlacementCreatedEntity(ctx, tx, commit, input.MentionRef)
	if err == nil {
		return existingEntityID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	identityFields := map[string]any{}
	for key, value := range input.IdentityContext {
		key = strings.TrimSpace(key)
		if key != "" {
			identityFields[key] = value
		}
	}
	identityFields["source"] = "semantic_placement"
	identityFields["mention_ref"] = input.MentionRef
	identityContext, err := marshalJSON(identityFields)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_records (team_id, entity_kind, identity_context, metadata)
		VALUES (?::uuid, ?, ?::jsonb, '{}'::jsonb)
		RETURNING entity_id::text
	`, commit.TeamID, input.EntityKind, string(identityContext)).Rows()
	if err != nil {
		return "", err
	}
	if !rows.Next() {
		_ = rows.Close()
		return "", rows.Err()
	}
	var entityID string
	if err := rows.Scan(&entityID); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	_, err = insertEntityName(ctx, tx, AddEntityNameInput{
		TeamID:         commit.TeamID,
		OwnerProfileID: commit.OwnerProfileID,
		EntityID:       entityID,
		DisplayName:    input.CanonicalName,
		NameKind:       "canonical",
	})
	if err != nil {
		return "", err
	}
	return entityID, nil
}

func loadPlacementCreatedEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	mentionRef string,
) (string, error) {
	var entityID string
	err := tx.WithContext(ctx).Raw(`
		SELECT event.entity_id::text
		FROM entity_resolution_events AS event
		JOIN entity_records AS entity
		  ON entity.team_id = event.team_id
		 AND entity.entity_id = event.entity_id
		 AND entity.status = 'active'
		WHERE event.team_id = ?::uuid
		  AND event.owner_profile_id = ?::uuid
		  AND event.placement_item_id = ?::uuid
		  AND event.mention_ref = ?
		  AND event.action = 'create'
		  AND event.entity_id IS NOT NULL
		ORDER BY event.created_at, event.resolution_event_id
		LIMIT 1
	`, commit.TeamID, commit.OwnerProfileID, commit.PlacementItemID, mentionRef).Row().Scan(&entityID)
	if err != nil {
		return "", err
	}
	return entityID, nil
}

func resolvePlacementPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	decision ApplyRelationshipDecisionInput,
	candidate PlacementPredicateCandidateInput,
) (ApplyRelationshipDecisionInput, error) {
	canonicalKey := canonicalGeneratedPredicateKey(candidate.PredicateKey)
	canonicalOriginal := canonicalGeneratedPredicateKey(decision.OriginalPredicate)
	matches, err := loadPlacementPredicateMatches(
		ctx,
		tx,
		decision.TeamID,
		canonicalKey,
		decision.OriginalPredicate,
		canonicalOriginal,
	)
	if err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	if len(matches) == 0 {
		if err := tx.WithContext(ctx).Exec(
			`SELECT pg_advisory_xact_lock(hashtext(?))`,
			decision.TeamID+":"+canonicalKey,
		).Error; err != nil {
			return ApplyRelationshipDecisionInput{}, err
		}
		matches, err = loadPlacementPredicateMatches(
			ctx,
			tx,
			decision.TeamID,
			canonicalKey,
			decision.OriginalPredicate,
			canonicalOriginal,
		)
		if err != nil {
			return ApplyRelationshipDecisionInput{}, err
		}
	}
	if len(matches) > 1 {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q resolves to multiple team definitions",
			errPlacementPredicateReview,
			decision.OriginalPredicate,
		)
	}
	if len(matches) == 0 && !placementNovelPredicateSafe(
		candidate.PredicateKey,
		canonicalKey,
		decision.OriginalPredicate,
	) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate candidate %q is not a safe canonical key",
			errPlacementPredicateReview,
			candidate.PredicateKey,
		)
	}

	subjectKind, objectKind, err := loadPlacementPredicateEndpointKinds(ctx, tx, decision)
	if err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	var resolved SemanticReviewPredicateCandidate
	if len(matches) == 1 {
		resolved = matches[0]
	} else {
		inserted, err := insertTeamPredicateCandidate(ctx, tx, EnsureSemanticPredicateCandidateInput{
			TeamID:           decision.TeamID,
			OwnerProfileID:   decision.OwnerProfileID,
			Predicate:        decision.OriginalPredicate,
			RelationshipKind: candidate.RelationshipKind,
			SubjectKind:      subjectKind,
			ObjectKind:       objectKind,
			Origin:           "provider_generated",
			Metadata: map[string]any{
				"source":                   "semantic_placement",
				"predicate_policy_version": domain.PredicatePolicyVersion,
				"ingest_id":                decision.IngestID,
				"placement_item_id":        decision.PlacementItemID,
			},
		}, canonicalKey, false)
		if err != nil {
			return ApplyRelationshipDecisionInput{}, err
		}
		resolved = *inserted
	}
	if resolved.LifecycleState != string(domain.PredicateLifecycleActive) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q lifecycle is %q",
			errPlacementPredicateReview,
			resolved.PredicateKey,
			resolved.LifecycleState,
		)
	}
	if resolved.RelationshipKind != candidate.RelationshipKind {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q relationship_kind is %q, candidate requested %q",
			errPlacementPredicateReview,
			resolved.PredicateKey,
			resolved.RelationshipKind,
			candidate.RelationshipKind,
		)
	}
	if !placementPredicateKindAllowed(resolved.AllowedSubjectKinds, subjectKind) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q does not allow subject kind %q",
			errPlacementPredicateReview,
			resolved.PredicateKey,
			subjectKind,
		)
	}
	if !placementPredicateKindAllowed(resolved.AllowedObjectKinds, objectKind) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q does not allow object kind %q",
			errPlacementPredicateReview,
			resolved.PredicateKey,
			objectKind,
		)
	}
	decision.PredicateKey = resolved.PredicateKey
	decision.PredicateVersion = resolved.Version
	return decision, nil
}

func loadPlacementPredicateMatches(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	canonicalKey string,
	originalPredicate string,
	canonicalOriginal string,
) ([]SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest AS (
		    SELECT predicate_key, version, aliases,
		           allowed_subject_kinds, allowed_object_kinds,
		           relationship_kind, current_cardinality, lifecycle_state,
		           row_number() OVER (PARTITION BY predicate_key ORDER BY version DESC) AS version_rank
		    FROM team_predicate_definitions
		    WHERE team_id = ?::uuid
		), matched AS (
		    SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		           relationship_kind, current_cardinality, lifecycle_state
		    FROM latest
		    WHERE version_rank = 1
		      AND (
		          predicate_key IN (?, ?)
		          OR ? = ANY(aliases)
		          OR ? = ANY(aliases)
		          OR ? = ANY(aliases)
		      )
		)
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality, lifecycle_state
		FROM matched
		ORDER BY predicate_key
	`, teamID, canonicalKey, canonicalOriginal, canonicalKey, originalPredicate, canonicalOriginal).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSemanticReviewPredicateCandidates(rows)
}

func loadPlacementPredicateEndpointKinds(
	ctx context.Context,
	tx *gorm.DB,
	decision ApplyRelationshipDecisionInput,
) (string, string, error) {
	subjectKind, err := loadEntityKind(ctx, tx, decision.TeamID, decision.SubjectEntityID)
	if err != nil {
		return "", "", err
	}
	if decision.ObjectEntityID != "" {
		objectKind, err := loadEntityKind(ctx, tx, decision.TeamID, decision.ObjectEntityID)
		return subjectKind, objectKind, err
	}
	objectKind, err := loadValueType(ctx, tx, decision.TeamID, decision.ObjectValueID)
	return subjectKind, objectKind, err
}

func placementPredicateKindAllowed(allowed []string, actual string) bool {
	return len(allowed) == 0 || contains(allowed, actual)
}

func placementNovelPredicateSafe(candidateKey string, canonicalKey string, originalPredicate string) bool {
	candidateKey = strings.TrimSpace(candidateKey)
	originalPredicate = strings.TrimSpace(originalPredicate)
	if candidateKey == "" || candidateKey != canonicalKey || originalPredicate == "" ||
		len([]rune(candidateKey)) > 64 || len([]rune(originalPredicate)) > 128 {
		return false
	}
	for _, r := range candidateKey {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func applyRelationshipDecisionInTx(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
) (*RelationshipDecisionResult, error) {
	input = normalizeApplyRelationshipDecisionInput(input)
	predicate, err := loadPredicateDefinition(ctx, tx, input.TeamID, input.PredicateKey, input.PredicateVersion)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return insertPredicateReview(ctx, tx, input)
	}
	if err != nil {
		return nil, err
	}
	if err := validateRelationshipEndpointKinds(ctx, tx, input, predicate); err != nil {
		return nil, err
	}
	tier, status := tierStatusForVerdict(input.EvidenceVerdict, input.PromoteToFact)
	groupKey := semanticGroupKey(input)
	recordState, err := upsertRelationshipRecord(ctx, tx, input, predicate, tier, status, groupKey)
	if err != nil {
		return nil, err
	}
	if recordState.ValidToConflict {
		return insertRelationshipValidToReview(ctx, tx, input, recordState.Record)
	}
	observationID, err := insertRelationshipObservation(ctx, tx, input, recordState.Record.RelationshipID)
	if err != nil {
		return nil, err
	}
	verificationID, err := insertVerificationEvent(ctx, tx, input, observationID)
	if err != nil {
		return nil, err
	}
	var supportID, supportDecisionID string
	if input.EvidenceVerdict == string(domain.VerificationEntailed) && input.Support != nil {
		supportID, supportDecisionID, err = insertRelationshipSupport(ctx, tx, input, recordState.Record.RelationshipID, observationID, verificationID)
		if err != nil {
			return nil, err
		}
		if err := refreshRelationshipSupportCounts(ctx, tx, input.TeamID, recordState.Record.RelationshipID); err != nil {
			return nil, err
		}
	}
	if recordState.Changed {
		if _, err := insertRelationshipTransition(ctx, tx, transitionInput{
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
			IdempotencyKey:      relationshipTransitionIdempotencyKey(verificationID, supportDecisionID),
		}); err != nil {
			return nil, err
		}
	}
	loaded, err := loadRelationshipRecord(ctx, tx, input.TeamID, recordState.Record.RelationshipID)
	if err != nil {
		return nil, err
	}
	return &RelationshipDecisionResult{
		Relationship:        loaded,
		ObservationID:       observationID,
		VerificationEventID: verificationID,
		SupportID:           supportID,
		SupportDecisionID:   supportDecisionID,
		CreatedRelationship: recordState.Created,
	}, nil
}

func withPlacementDecisionScope(input CommitPlacementSemanticInput, decision ApplyRelationshipDecisionInput) ApplyRelationshipDecisionInput {
	decision.TeamID = input.TeamID
	decision.OwnerProfileID = input.OwnerProfileID
	decision.IngestID = input.IngestID
	decision.PlacementItemID = input.PlacementItemID
	return decision
}

func upsertPlacementRelationshipSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	relationship *RelationshipRecord,
	embeddingJobMaxAttempts int,
) (*SearchDocumentResult, error) {
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID:         commit.TeamID,
		OwnerProfileID: commit.OwnerProfileID,
		SourceKind:     "relationship",
		SourceID:       relationship.RelationshipID,
		SourceVersion:  int64(relationship.Version),
		DocumentText:   placementRelationshipSearchText(relationship),
	})
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	return upsertSearchDocumentInTx(ctx, tx, input, contract, embeddingJobMaxAttempts)
}

func placementCommitPayload(base map[string]any, result *CommitPlacementSemanticResult) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
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
	payload["relationship_outcomes"] = placementRelationshipOutcomePayload(result.RelationshipResults)
	payload["search_document_ids"] = searchDocuments
	payload["embedding_job_ids"] = embeddingJobs
	payload["entity_resolution_ids"] = append([]string(nil), result.EntityResolutionIDs...)
	payload["review_task_ids"] = append([]string(nil), result.ReviewTaskIDs...)
	return payload
}

func relationshipOutcomeCategory(result *RelationshipDecisionResult) string {
	if result == nil {
		return string(domain.OutcomeRelationshipRejected)
	}
	if result.ReviewTaskID != "" && result.Relationship == nil {
		switch result.Category {
		case string(domain.OutcomeIdentityNeedsReview):
			return string(domain.OutcomeIdentityNeedsReview)
		case string(domain.OutcomePredicateNeedsReview):
			return string(domain.OutcomePredicateNeedsReview)
		default:
			return string(domain.OutcomeRelationshipNeedsReview)
		}
	}
	if result.Relationship == nil {
		return string(domain.OutcomeRelationshipRejected)
	}
	switch result.Relationship.Status {
	case string(domain.RelationshipStatusPendingEvidence):
		return string(domain.OutcomeRelationshipPendingEvidence)
	case string(domain.RelationshipStatusRejected):
		return string(domain.OutcomeRelationshipRejected)
	}
	if result.Relationship.Tier == string(domain.RelationshipTierFact) {
		return string(domain.OutcomeRelationshipFact)
	}
	return string(domain.OutcomeRelationshipValidatedClaim)
}

func relationshipOutcomeReason(decision ApplyRelationshipDecisionInput, result *RelationshipDecisionResult) string {
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
	case string(domain.RelationshipStatusPendingEvidence):
		return "evidence was insufficient for active knowledge"
	case string(domain.RelationshipStatusRejected):
		return "evidence contradicted the proposed relationship"
	default:
		return "relationship accepted from verified evidence"
	}
}

func placementRelationshipOutcomePayload(results []RelationshipDecisionResult) []map[string]any {
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

func finishPlacementRunIfTerminal(ctx context.Context, tx *gorm.DB, input CommitPlacementSemanticInput, status string) error {
	var openCount, reviewCount int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT
		    COUNT(*) FILTER (WHERE status IN ('queued', 'processing')),
		    COUNT(*) FILTER (WHERE status = 'awaiting_review')
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
	`, input.TeamID, input.PlacementRunID).Row().Scan(&openCount, &reviewCount); err != nil {
		return err
	}
	if openCount > 0 {
		result := tx.WithContext(ctx).Exec(`
			UPDATE placement_runs
			SET status = `+placementRunGuardedStatusCase+`,
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
			return ErrPlacementLeaseLost
		}
		return nil
	}
	runStatus := status
	if reviewCount > 0 && status != string(domain.PlacementRunFailed) && status != string(domain.PlacementRunQuarantined) {
		runStatus = string(domain.PlacementRunAwaitingReview)
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
	`, runStatus, input.TeamID, input.PlacementRunID, input.OwnerProfileID, input.WorkerID, input.ExpectedAttempts)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPlacementLeaseLost
	}
	return nil
}

func requeuePlacementRunForRetry(ctx context.Context, tx *gorm.DB, input CommitPlacementSemanticInput) error {
	retryDelaySeconds := int(placementRetryDelay(input.ExpectedAttempts, input.PlacementItemID) / time.Second)
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = `+placementRunGuardedStatusCase+`,
		    worker_id = '',
		    lease_until = NULL,
		    available_at = now() + (? * interval '1 second'),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND status = 'processing'
		  AND worker_id = ?
		  AND attempts = ?
		  AND lease_until > clock_timestamp()
	`, retryDelaySeconds, input.TeamID, input.PlacementRunID, input.OwnerProfileID, input.WorkerID, input.ExpectedAttempts)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPlacementLeaseLost
	}
	return nil
}

func placementRetryDelay(attempt int, placementItemID string) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 15 * time.Second
	for i := 1; i < attempt; i++ {
		base *= 2
		if base >= 300*time.Second {
			base = 300 * time.Second
			break
		}
	}
	delay := base + time.Duration(placementRetryJitterSeconds(placementItemID))*time.Second
	if delay > 300*time.Second {
		return 300 * time.Second
	}
	return delay
}

func placementRetryJitterSeconds(placementItemID string) int {
	sum := sha256.Sum256([]byte(strings.TrimSpace(placementItemID)))
	return int(sum[0] % 15)
}

func intPointerArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
