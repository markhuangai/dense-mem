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

func appendV2PlacementReviewTaskID(result *V2CommitPlacementSemanticResult, taskID string) {
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

func appendV2PlacementRelationshipResult(result *V2CommitPlacementSemanticResult, relationship *V2RelationshipDecisionResult) {
	if result == nil || relationship == nil {
		return
	}
	result.RelationshipResults = append(result.RelationshipResults, *relationship)
}

func applyV2PlacementRelationshipDecision(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	decision V2ApplyRelationshipDecisionInput,
	correctionTarget *V2PlacementCorrectionTargetInput,
	placementFragmentID string,
	embeddingJobMaxAttempts int,
	result *V2CommitPlacementSemanticResult,
) error {
	applied, err := applyV2RelationshipDecisionInTx(ctx, tx, decision)
	if err != nil {
		return err
	}
	applied.ProposalID = decision.ProposalRef
	applied.OwnerProfileID = commit.OwnerProfileID
	applied.Category = v2RelationshipOutcomeCategory(applied)
	applied.Reason = v2RelationshipOutcomeReason(decision, applied)
	result.RelationshipResults = append(result.RelationshipResults, *applied)
	appendV2PlacementReviewTaskID(result, applied.ReviewTaskID)
	if applied.Relationship == nil || applied.Relationship.Status != string(domain.V2RelationshipStatusActive) {
		return nil
	}
	if correctionTarget != nil {
		if err := appendV2PlacementCorrectionTarget(ctx, tx, commit, applied, *correctionTarget); err != nil {
			return err
		}
	}
	if applied.SupportID != "" && decision.Support != nil && decision.Support.FragmentID != "" {
		if placementFragmentID == "" {
			var err error
			placementFragmentID, err = loadV2PlacementItemFragmentID(ctx, tx, commit)
			if err != nil {
				return err
			}
		}
		if decision.Support.FragmentID != placementFragmentID {
			document, err := upsertV2PlacementEvidenceSearchDocument(
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
			appendV2PlacementSearchDocument(result, document)
		}
	}
	document, err := upsertV2PlacementRelationshipSearchDocument(
		ctx,
		tx,
		commit,
		applied.Relationship,
		embeddingJobMaxAttempts,
	)
	if err != nil {
		return err
	}
	appendV2PlacementSearchDocument(result, document)
	return nil
}

func v2PlacementEvidenceSearchableStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case string(domain.V2SemanticReviewAccepted), string(domain.V2SemanticReviewReviewRequired):
		return true
	default:
		return false
	}
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

func insertV2PlacementEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementEntityResolutionInput,
) (string, error) {
	existingEntityID, err := loadV2PlacementCreatedEntity(ctx, tx, commit, input.MentionRef)
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
	identityContext, err := marshalV2JSON(identityFields)
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
	_, err = insertV2EntityName(ctx, tx, V2AddEntityNameInput{
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

func loadV2PlacementCreatedEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
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

func resolveV2PlacementPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	decision V2ApplyRelationshipDecisionInput,
	candidate V2PlacementPredicateCandidateInput,
) (V2ApplyRelationshipDecisionInput, error) {
	canonicalKey := canonicalV2GeneratedPredicateKey(candidate.PredicateKey)
	canonicalOriginal := canonicalV2GeneratedPredicateKey(decision.OriginalPredicate)
	matches, err := loadV2PlacementPredicateMatches(
		ctx,
		tx,
		decision.TeamID,
		canonicalKey,
		decision.OriginalPredicate,
		canonicalOriginal,
	)
	if err != nil {
		return V2ApplyRelationshipDecisionInput{}, err
	}
	if len(matches) == 0 {
		if err := tx.WithContext(ctx).Exec(
			`SELECT pg_advisory_xact_lock(hashtext(?))`,
			decision.TeamID+":"+canonicalKey,
		).Error; err != nil {
			return V2ApplyRelationshipDecisionInput{}, err
		}
		matches, err = loadV2PlacementPredicateMatches(
			ctx,
			tx,
			decision.TeamID,
			canonicalKey,
			decision.OriginalPredicate,
			canonicalOriginal,
		)
		if err != nil {
			return V2ApplyRelationshipDecisionInput{}, err
		}
	}
	if len(matches) > 1 {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q resolves to multiple team definitions",
			errV2PlacementPredicateReview,
			decision.OriginalPredicate,
		)
	}
	if len(matches) == 0 && !v2PlacementNovelPredicateSafe(
		candidate.PredicateKey,
		canonicalKey,
		decision.OriginalPredicate,
	) {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate candidate %q is not a safe canonical key",
			errV2PlacementPredicateReview,
			candidate.PredicateKey,
		)
	}

	subjectKind, objectKind, err := loadV2PlacementPredicateEndpointKinds(ctx, tx, decision)
	if err != nil {
		return V2ApplyRelationshipDecisionInput{}, err
	}
	var resolved V2SemanticReviewPredicateCandidate
	if len(matches) == 1 {
		resolved = matches[0]
	} else {
		inserted, err := insertV2TeamPredicateCandidate(ctx, tx, V2EnsureSemanticPredicateCandidateInput{
			TeamID:           decision.TeamID,
			OwnerProfileID:   decision.OwnerProfileID,
			Predicate:        decision.OriginalPredicate,
			RelationshipKind: candidate.RelationshipKind,
			SubjectKind:      subjectKind,
			ObjectKind:       objectKind,
			Origin:           "provider_generated",
			Metadata: map[string]any{
				"source":                   "semantic_placement",
				"predicate_policy_version": domain.V2PredicatePolicyVersion,
				"ingest_id":                decision.IngestID,
				"placement_item_id":        decision.PlacementItemID,
			},
		}, canonicalKey, false)
		if err != nil {
			return V2ApplyRelationshipDecisionInput{}, err
		}
		resolved = *inserted
	}
	if resolved.LifecycleState != string(domain.V2PredicateLifecycleActive) {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q lifecycle is %q",
			errV2PlacementPredicateReview,
			resolved.PredicateKey,
			resolved.LifecycleState,
		)
	}
	if resolved.RelationshipKind != candidate.RelationshipKind {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q relationship_kind is %q, candidate requested %q",
			errV2PlacementPredicateReview,
			resolved.PredicateKey,
			resolved.RelationshipKind,
			candidate.RelationshipKind,
		)
	}
	if !v2PlacementPredicateKindAllowed(resolved.AllowedSubjectKinds, subjectKind) {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q does not allow subject kind %q",
			errV2PlacementPredicateReview,
			resolved.PredicateKey,
			subjectKind,
		)
	}
	if !v2PlacementPredicateKindAllowed(resolved.AllowedObjectKinds, objectKind) {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q does not allow object kind %q",
			errV2PlacementPredicateReview,
			resolved.PredicateKey,
			objectKind,
		)
	}
	decision.PredicateKey = resolved.PredicateKey
	decision.PredicateVersion = resolved.Version
	return decision, nil
}

func loadV2PlacementPredicateMatches(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	canonicalKey string,
	originalPredicate string,
	canonicalOriginal string,
) ([]V2SemanticReviewPredicateCandidate, error) {
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
	return scanV2SemanticReviewPredicateCandidates(rows)
}

func loadV2PlacementPredicateEndpointKinds(
	ctx context.Context,
	tx *gorm.DB,
	decision V2ApplyRelationshipDecisionInput,
) (string, string, error) {
	subjectKind, err := loadV2EntityKind(ctx, tx, decision.TeamID, decision.SubjectEntityID)
	if err != nil {
		return "", "", err
	}
	if decision.ObjectEntityID != "" {
		objectKind, err := loadV2EntityKind(ctx, tx, decision.TeamID, decision.ObjectEntityID)
		return subjectKind, objectKind, err
	}
	objectKind, err := loadV2ValueType(ctx, tx, decision.TeamID, decision.ObjectValueID)
	return subjectKind, objectKind, err
}

func v2PlacementPredicateKindAllowed(allowed []string, actual string) bool {
	return len(allowed) == 0 || v2Contains(allowed, actual)
}

func v2PlacementNovelPredicateSafe(candidateKey string, canonicalKey string, originalPredicate string) bool {
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

func applyV2RelationshipDecisionInTx(
	ctx context.Context,
	tx *gorm.DB,
	input V2ApplyRelationshipDecisionInput,
) (*V2RelationshipDecisionResult, error) {
	input = normalizeV2ApplyRelationshipDecisionInput(input)
	predicate, err := loadV2PredicateDefinition(ctx, tx, input.TeamID, input.PredicateKey, input.PredicateVersion)
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
	embeddingJobMaxAttempts int,
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
	return upsertV2SearchDocumentInTx(ctx, tx, input, contract, embeddingJobMaxAttempts)
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
	payload["review_task_ids"] = append([]string(nil), result.ReviewTaskIDs...)
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
	runStatus := status
	if reviewCount > 0 && status != string(domain.V2PlacementRunFailed) && status != string(domain.V2PlacementRunQuarantined) {
		runStatus = string(domain.V2PlacementRunAwaitingReview)
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
		return ErrV2PlacementLeaseLost
	}
	return nil
}

func requeueV2PlacementRunForRetry(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) error {
	retryDelaySeconds := int(v2PlacementRetryDelay(input.ExpectedAttempts, input.PlacementItemID) / time.Second)
	result := tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET status = `+v2PlacementRunGuardedStatusCase+`,
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
		return ErrV2PlacementLeaseLost
	}
	return nil
}

func v2PlacementRetryDelay(attempt int, placementItemID string) time.Duration {
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
	delay := base + time.Duration(v2PlacementRetryJitterSeconds(placementItemID))*time.Second
	if delay > 300*time.Second {
		return 300 * time.Second
	}
	return delay
}

func v2PlacementRetryJitterSeconds(placementItemID string) int {
	sum := sha256.Sum256([]byte(strings.TrimSpace(placementItemID)))
	return int(sum[0] % 15)
}

func v2IntPointerArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
