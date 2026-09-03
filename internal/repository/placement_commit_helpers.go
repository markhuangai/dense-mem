package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func appendSemanticSearchDocument(result *submissionSemanticCommitState, document *SearchDocumentResult) {
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

func applySemanticRelationshipDecision(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitSemanticInput,
	decision ApplyRelationshipDecisionInput,
	correctionTarget *SemanticCorrectionTargetInput,
	conflictContext *SemanticConflictContextInput,
	semanticFragmentID string,
	conflictConfig ConflictRuntimeConfig,
	result *submissionSemanticCommitState,
) error {
	applied, err := applyRelationshipDecisionInTx(ctx, tx, decision)
	if errors.Is(err, errRelationshipDecisionNonPromotable) {
		return ErrSubmissionAssessmentNonPromotable
	}
	if err != nil {
		return err
	}
	applied.ProposalID = decision.ProposalRef
	applied.OwnerProfileID = commit.OwnerProfileID
	applied.Category = relationshipOutcomeCategory(applied)
	applied.Reason = relationshipOutcomeReason(decision, applied)
	applied.ConfidenceGate = decision.GateResult
	applied.PolicyVersion = decision.AssessmentPolicyVersion
	result.RelationshipResults = append(result.RelationshipResults, *applied)
	if applied.Relationship == nil || applied.Relationship.Status != string(domain.RelationshipStatusActive) {
		return nil
	}
	if correctionTarget != nil {
		if err := appendSemanticCorrectionTarget(ctx, tx, commit, applied, *correctionTarget); err != nil {
			return err
		}
	}
	supports := relationshipEvidenceSupports(decision.Support, decision.Supports)
	for index, support := range supports {
		if index >= len(applied.SupportIDs) || applied.SupportIDs[index] == "" || support.FragmentID == "" {
			continue
		}
		// A non-empty evidence owner identifies read-only known context. Its
		// existing search projection must not be rewritten for this submission.
		if support.EvidenceOwnerProfileID != "" {
			continue
		}
		if semanticFragmentID == "" {
			var err error
			semanticFragmentID, err = loadSemanticItemFragmentID(ctx, tx, commit)
			if err != nil {
				return err
			}
		}
		if support.FragmentID != semanticFragmentID {
			document, err := upsertSemanticEvidenceSearchDocument(
				ctx,
				tx,
				commit,
				support.FragmentID,
				map[string]any{
					"supporting_fragment_id": semanticFragmentID,
					"support_id":             applied.SupportIDs[index],
					"relationship_id":        applied.Relationship.RelationshipID,
				},
			)
			if err != nil {
				return err
			}
			appendSemanticSearchDocument(result, document)
		}
	}
	document, err := upsertRelationshipSearchDocument(
		ctx,
		tx,
		commit,
		applied.Relationship,
	)
	if err != nil {
		return err
	}
	appendSemanticSearchDocument(result, document)
	if err := applyRelationshipConflictPlacement(ctx, tx, commit, applied, conflictConfig); err != nil {
		return err
	}
	return nil
}

type semanticCorrectionTargetRecord struct {
	SubjectEntityID string
	PredicateKey    string
	ObjectEntityID  string
	ObjectValueID   string
}

func appendSemanticCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitSemanticInput,
	applied *RelationshipDecisionResult,
	target SemanticCorrectionTargetInput,
) error {
	if applied == nil || applied.Relationship == nil || applied.VerificationEventID == "" {
		return errors.New("correction target requires an applied source relationship and verification event")
	}
	source := applied.Relationship
	if err := requireRelationshipVersion(ctx, tx, commit.TeamID, source.RelationshipID, commit.OwnerProfileID, source.Version); err != nil {
		return err
	}
	if err := requireRelationshipVersion(ctx, tx, commit.TeamID, target.RelationshipID, commit.OwnerProfileID, target.ExpectedVersion); err != nil {
		if errors.Is(err, errRelationshipVersionMismatch) {
			return fmt.Errorf("%w: correction target changed", ErrCorrectionTargetStale)
		}
		return err
	}
	if err := requireVerificationForRelationship(ctx, tx, commit.TeamID, applied.VerificationEventID, commit.OwnerProfileID, source.RelationshipID); err != nil {
		return err
	}
	sourceSpaceID, err := loadRelationshipSpaceID(ctx, tx, commit.TeamID, source.RelationshipID, source.Version)
	if err != nil {
		return err
	}
	targetRecord, err := loadSemanticCorrectionTarget(ctx, tx, commit.TeamID, target)
	if err != nil {
		return err
	}
	if !semanticCorrectionTargetRelated(source, targetRecord) {
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
		    target_relationship_version, kind, verification_event_id, metadata, space_id
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?::uuid, ?::jsonb, ?::uuid
		)
		RETURNING cross_reference_id::text
	`, commit.TeamID, commit.OwnerProfileID, source.RelationshipID, source.Version,
		target.RelationshipID, target.ExpectedVersion, string(domain.CrossReferenceCorrects),
		applied.VerificationEventID, string(metadata), sourceSpaceID).Rows()
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

func loadSemanticCorrectionTarget(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	target SemanticCorrectionTargetInput,
) (semanticCorrectionTargetRecord, error) {
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
		return semanticCorrectionTargetRecord{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return semanticCorrectionTargetRecord{}, err
		}
		return semanticCorrectionTargetRecord{}, sql.ErrNoRows
	}
	var record semanticCorrectionTargetRecord
	if err := rows.Scan(&record.SubjectEntityID, &record.PredicateKey, &record.ObjectEntityID, &record.ObjectValueID); err != nil {
		return semanticCorrectionTargetRecord{}, err
	}
	return record, rows.Err()
}

func semanticCorrectionTargetRelated(source *RelationshipRecord, target semanticCorrectionTargetRecord) bool {
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

func insertSemanticEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitSemanticInput,
	input SemanticEntityResolutionInput,
) (string, error) {
	existingEntityID, err := loadSemanticCreatedEntity(ctx, tx, commit, input.MentionRef)
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
	identityFields["source"] = "semantic_commit"
	identityFields["mention_ref"] = input.MentionRef
	identityContext, err := marshalJSON(identityFields)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_records (team_id, entity_kind, identity_context, metadata, space_id, space_generation)
		SELECT ?::uuid, ?, ?::jsonb, '{}'::jsonb, ingest.space_id, ingest.space_generation
		FROM knowledge_ingests AS ingest
		WHERE ingest.team_id = ?::uuid
		  AND ingest.ingest_id = ?::uuid
		  AND ingest.owner_profile_id = ?::uuid
		RETURNING entity_id::text
	`, commit.TeamID, input.EntityKind, string(identityContext), commit.TeamID, commit.IngestID, commit.OwnerProfileID).Rows()
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

func loadSemanticCreatedEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitSemanticInput,
	mentionRef string,
) (string, error) {
	var entityID string
	query := `
		SELECT event.entity_id::text
		FROM entity_resolution_events AS event
		JOIN entity_records AS entity
		  ON entity.team_id = event.team_id
		 AND entity.entity_id = event.entity_id
		 AND entity.status = 'active'
		WHERE event.team_id = ?::uuid
		  AND event.owner_profile_id = ?::uuid
		  AND event.mention_ref = ?
		  AND event.action = 'create'
		  AND event.entity_id IS NOT NULL
		  AND %s
		ORDER BY event.created_at, event.resolution_event_id
		LIMIT 1`
	query = fmt.Sprintf(query, "event.ingest_id = ?::uuid AND event.fragment_id = ?::uuid")
	args := []any{commit.TeamID, commit.OwnerProfileID, mentionRef, commit.IngestID, commit.FragmentID}
	err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(&entityID)
	if err != nil {
		return "", err
	}
	return entityID, nil
}

func resolveSemanticPredicateCandidate(
	ctx context.Context,
	tx *gorm.DB,
	decision ApplyRelationshipDecisionInput,
	candidate SemanticPredicateCandidateInput,
) (ApplyRelationshipDecisionInput, error) {
	canonicalKey := canonicalGeneratedPredicateKey(candidate.PredicateKey)
	canonicalOriginal := canonicalGeneratedPredicateKey(decision.OriginalPredicate)
	matches, err := loadSemanticPredicateMatches(
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
		matches, err = loadSemanticPredicateMatches(
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
			errSemanticPredicateUnresolved,
			decision.OriginalPredicate,
		)
	}
	if len(matches) == 0 && !semanticNovelPredicateSafe(
		candidate.PredicateKey,
		canonicalKey,
		decision.OriginalPredicate,
	) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate candidate %q is not a safe canonical key",
			errSemanticPredicateUnresolved,
			candidate.PredicateKey,
		)
	}

	subjectKind, objectKind, err := loadSemanticPredicateEndpointKinds(ctx, tx, decision)
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
				"source":                   "semantic_commit",
				"predicate_policy_version": domain.PredicatePolicyVersion,
				"ingest_id":                decision.IngestID,
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
			errSemanticPredicateUnresolved,
			resolved.PredicateKey,
			resolved.LifecycleState,
		)
	}
	if resolved.RelationshipKind != candidate.RelationshipKind {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q relationship_kind is %q, candidate requested %q",
			errSemanticPredicateUnresolved,
			resolved.PredicateKey,
			resolved.RelationshipKind,
			candidate.RelationshipKind,
		)
	}
	if !semanticPredicateKindAllowed(resolved.AllowedSubjectKinds, subjectKind) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q does not allow subject kind %q",
			errSemanticPredicateUnresolved,
			resolved.PredicateKey,
			subjectKind,
		)
	}
	if !semanticPredicateKindAllowed(resolved.AllowedObjectKinds, objectKind) {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf(
			"%w: predicate %q does not allow object kind %q",
			errSemanticPredicateUnresolved,
			resolved.PredicateKey,
			objectKind,
		)
	}
	decision.PredicateKey = resolved.PredicateKey
	decision.PredicateVersion = resolved.Version
	return decision, nil
}

func loadSemanticPredicateMatches(
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

func loadSemanticPredicateEndpointKinds(
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

func semanticPredicateKindAllowed(allowed []string, actual string) bool {
	return len(allowed) == 0 || contains(allowed, actual)
}

func semanticNovelPredicateSafe(candidateKey string, canonicalKey string, originalPredicate string) bool {
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
	if err := validateSupportOwnership(ctx, tx, input); err != nil {
		return nil, err
	}
	predicate, err := loadPredicateDefinition(ctx, tx, input.TeamID, input.PredicateKey, input.PredicateVersion)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: predicate %q version %d is not active", errRelationshipDecisionNonPromotable, input.PredicateKey, input.PredicateVersion)
	}
	if err != nil {
		return nil, err
	}
	if err := validateRelationshipEndpointKinds(ctx, tx, input, predicate); err != nil {
		return nil, err
	}
	status := statusForRelationshipDecision(input)
	groupKey := semanticGroupKey(input)
	recordState, err := upsertRelationshipRecord(ctx, tx, input, predicate, status, groupKey)
	if err != nil {
		return nil, err
	}
	if recordState.ValidToConflict {
		return nil, fmt.Errorf("%w: relationship valid_to conflicts with current state", errRelationshipDecisionNonPromotable)
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
	var supportIDs []string
	if (input.AssessorAccepted || input.EvidenceVerdict == string(domain.VerificationEntailed)) && len(relationshipEvidenceSupports(input.Support, input.Supports)) > 0 && !input.SuppressSupport {
		var supportDecisionIDs []string
		supportIDs, supportDecisionIDs, err = insertRelationshipSupports(ctx, tx, input, recordState.Record.RelationshipID, observationID, verificationID)
		if err != nil {
			return nil, err
		}
		if len(supportIDs) > 0 {
			supportID = supportIDs[0]
		}
		if len(supportDecisionIDs) > 0 {
			supportDecisionID = supportDecisionIDs[0]
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
			FromStatus:          recordState.FromStatus,
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
		SupportIDs:          supportIDs,
		SupportDecisionID:   supportDecisionID,
		CreatedRelationship: recordState.Created,
	}, nil
}

func upsertRelationshipSearchDocument(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitSemanticInput,
	relationship *RelationshipRecord,
) (*SearchDocumentResult, error) {
	if !relationshipSearchEligible(relationship) {
		return markRelationshipSearchDocumentNotRequired(ctx, tx, commit, relationship)
	}
	contract, err := loadActiveSearchContractInTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	text, err := semanticRelationshipSearchText(ctx, tx, relationship)
	if err != nil {
		return nil, err
	}
	previousGenerationID, err := relationshipSearchDocumentProjectionGenerationID(
		ctx,
		tx,
		commit.TeamID,
		relationship.RelationshipID,
		contract.EmbeddingContractID,
	)
	if err != nil {
		return nil, err
	}
	metadata, err := relationshipForegroundSearchMetadata(ctx, tx, commit.TeamID)
	if err != nil {
		return nil, err
	}
	input := normalizeUpsertSearchDocumentInput(UpsertSearchDocumentInput{
		TeamID:           commit.TeamID,
		OwnerProfileID:   commit.OwnerProfileID,
		SourceKind:       "relationship",
		SourceID:         relationship.RelationshipID,
		SourceVersion:    int64(relationship.Version),
		ProjectionFormat: 2,
		DocumentText:     text,
		Metadata:         metadata,
		SpaceID:          relationship.SpaceID,
		SpaceGeneration:  relationship.SpaceGeneration,
	})
	if err := validateUpsertSearchDocumentInput(input); err != nil {
		return nil, err
	}
	result, err := upsertSearchDocumentInTx(ctx, tx, input, contract)
	if err != nil {
		return nil, err
	}
	if err := refreshPreviousRelationshipProjectionGeneration(ctx, tx, commit.TeamID, previousGenerationID); err != nil {
		return nil, err
	}
	return result, nil
}

func semanticCommitPayload(base map[string]any, result *submissionSemanticCommitState) map[string]any {
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
	for _, item := range result.SearchDocuments {
		searchDocuments = append(searchDocuments, item.SearchDocumentID)
	}
	payload["relationship_ids"] = relationships
	payload["relationship_outcomes"] = semanticRelationshipOutcomePayload(result.RelationshipResults)
	payload["search_document_ids"] = searchDocuments
	payload["entity_resolution_ids"] = append([]string(nil), result.EntityResolutionIDs...)
	return payload
}

func relationshipOutcomeCategory(result *RelationshipDecisionResult) string {
	if result == nil {
		return string(domain.OutcomeRelationshipRejected)
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
	return string(domain.OutcomeRelationshipAccepted)
}

func relationshipOutcomeReason(decision ApplyRelationshipDecisionInput, result *RelationshipDecisionResult) string {
	if decision.GateResult == "below_write_threshold" {
		return "confidence was below the write threshold"
	}
	if rationale := strings.TrimSpace(decision.Rationale); rationale != "" {
		return rationale
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

func semanticRelationshipOutcomePayload(results []RelationshipDecisionResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		item := map[string]any{
			"proposal_id":      result.ProposalID,
			"observation_id":   result.ObservationID,
			"owner_profile_id": result.OwnerProfileID,
			"category":         result.Category,
			"reason":           result.Reason,
		}
		if result.ConfidenceGate != "" {
			item["confidence_gate"] = result.ConfidenceGate
		}
		if result.PolicyVersion != "" {
			item["policy_version"] = result.PolicyVersion
		}
		if result.Relationship != nil {
			item["relationship_id"] = result.Relationship.RelationshipID
			if result.Relationship.OwnerProfileID != "" {
				item["owner_profile_id"] = result.Relationship.OwnerProfileID
			}
			item["relationship_status"] = result.Relationship.Status
		}
		out = append(out, item)
	}
	return out
}

func intPointerArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
