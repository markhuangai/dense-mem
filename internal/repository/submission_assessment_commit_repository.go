package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type submissionLockedItem struct {
	PlacementItemID string
	FragmentID      string
	EvidenceIndex   int
	Status          string
	Category        string
	Fragment        EvidenceFragment
	SourceStale     bool
}

func (r *LedgerRepositoryImpl) CommitSubmissionAssessment(
	ctx context.Context,
	input CommitSubmissionAssessmentInput,
) (*CommitSubmissionAssessmentResult, error) {
	input = normalizeCommitSubmissionAssessmentInput(input)
	if err := validateCommitSubmissionAssessmentInput(input); err != nil {
		return nil, err
	}
	result := &CommitSubmissionAssessmentResult{Status: string(domain.SemanticReviewAccepted)}
	semanticResult := &CommitPlacementSemanticResult{Status: string(domain.SemanticReviewAccepted)}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockSubmissionAssessmentRun(ctx, tx, input.SubmissionAssessmentRunScope); err != nil {
			return fmt.Errorf("lock submission assessment run: %w", err)
		}
		if err := validateSubmissionAssessmentCommitScope(ctx, tx, input); err != nil {
			return err
		}
		items, err := loadLockedSubmissionAssessmentItems(ctx, tx, input.SubmissionAssessmentRunScope)
		if err != nil {
			return fmt.Errorf("lock submission assessment items: %w", err)
		}
		if err := validateSubmissionCommitItems(input.Items, items); err != nil {
			return fmt.Errorf("validate submission assessment items: %w", err)
		}
		fragmentIDs := make([]string, 0, len(items))
		for _, item := range items {
			fragmentIDs = append(fragmentIDs, item.FragmentID)
		}
		if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, fragmentIDs); err != nil {
			return err
		}
		for _, item := range items {
			if item.SourceStale {
				return ErrPlacementStaleSource
			}
		}
		for _, entry := range input.RelationshipObservations {
			conflictContext := entry.Observation.ConflictContext
			if conflictContext == nil {
				continue
			}
			if err := requireRelationshipConflictContextCurrent(
				ctx,
				tx,
				input.TeamID,
				conflictContext.ConflictID,
				conflictContext.ExpectedVersion,
			); err != nil {
				return err
			}
		}
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}

		itemByID := make(map[string]submissionLockedItem, len(items))
		fragmentItemIDs := make(map[string]struct{}, len(items))
		for _, item := range items {
			itemByID[item.PlacementItemID] = item
			fragmentItemIDs[item.FragmentID] = struct{}{}
		}
		if err := resolveSubmissionPredicates(ctx, tx, &input); err != nil {
			return err
		}

		common := CommitPlacementSemanticInput{
			TeamID:           input.TeamID,
			OwnerProfileID:   input.OwnerProfileID,
			IngestID:         input.IngestID,
			PlacementRunID:   input.PlacementRunID,
			WorkerID:         input.WorkerID,
			ExpectedAttempts: input.ExpectedAttempts,
			OutcomeKind:      "submission_assessment_commit",
			Status:           string(domain.SemanticReviewAccepted),
			Category:         "validated_claim",
		}
		entitiesByRef := make(map[string]string, len(input.EntityResolutions))
		for _, entry := range input.EntityResolutions {
			item, ok := itemByID[entry.PlacementItemID]
			if !ok {
				return ErrPlacementLeaseLost
			}
			commit := common
			commit.PlacementItemID = entry.PlacementItemID
			if entry.Resolution.FragmentID != item.FragmentID {
				return errors.New("submission entity resolution fragment does not match placement item")
			}
			resolutionID, entityID, err := insertPlacementEntityResolution(ctx, tx, commit, entry.Resolution)
			if err != nil {
				return err
			}
			if entityID == "" {
				return ErrSubmissionAssessmentNonPromotable
			}
			if _, exists := entitiesByRef[entry.Resolution.MentionRef]; exists {
				return errors.New("submission entity resolution mention_ref is duplicated")
			}
			entitiesByRef[entry.Resolution.MentionRef] = entityID
			semanticResult.EntityResolutionIDs = append(semanticResult.EntityResolutionIDs, resolutionID)
		}

		for _, entry := range input.RelationshipObservations {
			item, ok := itemByID[entry.PlacementItemID]
			if !ok {
				return ErrPlacementLeaseLost
			}
			for _, support := range relationshipEvidenceSupports(entry.Observation.Support, entry.Observation.Supports) {
				if _, ok := fragmentItemIDs[support.FragmentID]; !ok {
					return errors.New("submission relationship support is outside the placement run")
				}
			}
			commit := common
			commit.PlacementItemID = entry.PlacementItemID
			decision, err := relationshipDecisionFromPlacementObservation(ctx, tx, commit, entry.Observation, entitiesByRef)
			if err != nil {
				return err
			}
			if entry.Observation.ConflictContext != nil {
				if err := requireRelationshipConflictContextMatchesDecision(
					ctx,
					tx,
					input.TeamID,
					*entry.Observation.ConflictContext,
					decision,
				); err != nil {
					return err
				}
			}
			appliedBefore := len(semanticResult.RelationshipResults)
			if err := applyPlacementRelationshipDecision(
				ctx,
				tx,
				commit,
				decision,
				entry.Observation.CorrectionTarget,
				entry.Observation.ConflictContext,
				item.FragmentID,
				r.embeddingJobMaxAttempts,
				ConflictRuntimeConfig{ReviewTTLDays: r.conflictReviewTTLDays, Timezone: r.conflictReviewTimezone},
				semanticResult,
			); err != nil {
				return err
			}
			if len(semanticResult.RelationshipResults) != appliedBefore+1 {
				return ErrSubmissionAssessmentNonPromotable
			}
			applied := semanticResult.RelationshipResults[len(semanticResult.RelationshipResults)-1]
			if applied.ReviewTaskID != "" || applied.Relationship == nil || applied.Relationship.Status != string(domain.RelationshipStatusActive) {
				return ErrSubmissionAssessmentNonPromotable
			}
		}

		evidenceSearchContract, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, item := range items {
			commit := common
			commit.PlacementItemID = item.PlacementItemID
			document, err := upsertPlacementEvidenceSearchDocumentWithContract(
				ctx,
				tx,
				commit,
				item.FragmentID,
				map[string]any{"placement_item_id": commit.PlacementItemID},
				evidenceSearchContract,
				r.embeddingJobMaxAttempts,
			)
			if err != nil {
				return err
			}
			appendPlacementSearchDocument(semanticResult, document)
		}

		payload := placementCommitPayload(input.Payload, semanticResult)
		payload["submission_atomic"] = true
		for _, item := range items {
			itemPayload := cloneSubmissionPayload(payload)
			itemPayload["evidence_index"] = item.EvidenceIndex
			outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:          input.TeamID,
				OwnerProfileID:  input.OwnerProfileID,
				PlacementRunID:  input.PlacementRunID,
				PlacementItemID: item.PlacementItemID,
				OutcomeKind:     "submission_assessment_commit",
				Status:          string(domain.SemanticReviewAccepted),
				Payload:         itemPayload,
			})
			if err != nil {
				return err
			}
			if err := updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:             input.TeamID,
				OwnerProfileID:     input.OwnerProfileID,
				PlacementItemID:    item.PlacementItemID,
				UpdateItemStatus:   string(domain.PlacementRunCompleted),
				UpdateItemCategory: "validated_claim",
				Payload:            itemPayload,
			}); err != nil {
				return err
			}
			result.OutcomeIDs = append(result.OutcomeIDs, outcomeID)
		}
		if err := promoteSubmissionReplacement(ctx, tx, input.SubmissionAssessmentRunScope); err != nil {
			return err
		}
		firstDisposition, err := completeSubmissionPlacementRun(ctx, tx, input.SubmissionAssessmentRunScope, string(domain.PlacementRunCompleted), "")
		if err != nil {
			return err
		}
		result.FirstDisposition = firstDisposition
		result.RelationshipResults = append([]RelationshipDecisionResult(nil), semanticResult.RelationshipResults...)
		result.SearchDocuments = append([]SearchDocumentResult(nil), semanticResult.SearchDocuments...)
		result.EntityResolutionIDs = append([]string(nil), semanticResult.EntityResolutionIDs...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission assessment commit: %w", err)
	}
	return result, nil
}

func normalizeCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) CommitSubmissionAssessmentInput {
	input.SubmissionAssessmentRunScope = normalizeSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	for i := range input.Items {
		input.Items[i].PlacementItemID = strings.TrimSpace(input.Items[i].PlacementItemID)
		input.Items[i].FragmentID = strings.TrimSpace(input.Items[i].FragmentID)
	}
	for i := range input.EntityResolutions {
		entry := &input.EntityResolutions[i]
		entry.PlacementItemID = strings.TrimSpace(entry.PlacementItemID)
		entry.Resolution = normalizeSubmissionEntityResolution(entry.Resolution)
	}
	for i := range input.RelationshipObservations {
		entry := &input.RelationshipObservations[i]
		entry.PlacementItemID = strings.TrimSpace(entry.PlacementItemID)
		entry.Observation = normalizePlacementRelationshipDecisionInput(entry.Observation)
	}
	for i := range input.PredicateRegistrations {
		registration := &input.PredicateRegistrations[i]
		registration.RelationshipRef = strings.TrimSpace(registration.RelationshipRef)
		registration.PredicateKey = strings.TrimSpace(registration.PredicateKey)
		registration.SubjectKind = strings.TrimSpace(registration.SubjectKind)
		registration.ObjectKind = strings.TrimSpace(registration.ObjectKind)
	}
	return input
}

func normalizeSubmissionEntityResolution(input PlacementEntityResolutionInput) PlacementEntityResolutionInput {
	input.MentionRef = strings.TrimSpace(input.MentionRef)
	input.Action = strings.TrimSpace(input.Action)
	input.EntityID = strings.TrimSpace(input.EntityID)
	input.EntityKind = strings.TrimSpace(input.EntityKind)
	input.CanonicalName = strings.TrimSpace(input.CanonicalName)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	input.SemanticReviewKind = strings.TrimSpace(input.SemanticReviewKind)
	input.ReviewQuestion = strings.TrimSpace(input.ReviewQuestion)
	input.ReviewGuidance = strings.TrimSpace(input.ReviewGuidance)
	return input
}

func validateCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) error {
	if err := validateSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope); err != nil {
		return err
	}
	if _, err := uuid.Parse(input.AssessmentID); err != nil {
		return fmt.Errorf("assessment_id is required: %w", err)
	}
	if len(input.Items) == 0 {
		return errors.New("submission assessment items are required")
	}
	seenItems := map[string]struct{}{}
	for _, item := range input.Items {
		if _, err := uuid.Parse(item.PlacementItemID); err != nil {
			return fmt.Errorf("placement_item_id is required: %w", err)
		}
		if _, err := uuid.Parse(item.FragmentID); err != nil {
			return fmt.Errorf("fragment_id is required: %w", err)
		}
		if _, exists := seenItems[item.PlacementItemID]; exists {
			return errors.New("submission assessment placement_item_id is duplicated")
		}
		seenItems[item.PlacementItemID] = struct{}{}
	}
	seenEntities := map[string]struct{}{}
	for _, entry := range input.EntityResolutions {
		if _, err := uuid.Parse(entry.PlacementItemID); err != nil {
			return fmt.Errorf("entity resolution placement_item_id is required: %w", err)
		}
		if entry.Resolution.Action == string(domain.EntityResolutionAmbiguous) || entry.Resolution.SemanticReviewKind != "" {
			return ErrSubmissionAssessmentNonPromotable
		}
		if err := validatePlacementEntityResolutionInput(entry.Resolution); err != nil {
			return err
		}
		if entry.Resolution.AssessmentID != input.AssessmentID {
			return errors.New("submission entity resolution assessment_id does not match the submission assessment")
		}
		if _, exists := seenEntities[entry.Resolution.MentionRef]; exists {
			return errors.New("submission entity resolution mention_ref is duplicated")
		}
		seenEntities[entry.Resolution.MentionRef] = struct{}{}
	}
	seenRelationships := map[string]struct{}{}
	registrationByRef := map[string]SubmissionPredicateRegistrationInput{}
	for _, registration := range input.PredicateRegistrations {
		registrationByRef[registration.RelationshipRef] = registration
	}
	for _, entry := range input.RelationshipObservations {
		if _, err := uuid.Parse(entry.PlacementItemID); err != nil {
			return fmt.Errorf("relationship observation placement_item_id is required: %w", err)
		}
		if entry.Observation.PredicateCandidate != nil || entry.Observation.SemanticReviewKind != "" || entry.Observation.SuppressSupport || entry.Observation.EvidenceVerdict != string(domain.VerificationEntailed) || entry.Observation.Confidence == nil || math.IsNaN(*entry.Observation.Confidence) || math.IsInf(*entry.Observation.Confidence, 0) {
			return ErrSubmissionAssessmentNonPromotable
		}
		validationObservation := entry.Observation
		if registration, needsRegistration := registrationByRef[validationObservation.Ref]; needsRegistration {
			validationObservation.PredicateKey = registration.PredicateKey
			validationObservation.PredicateVersion = 1
		}
		if err := validatePlacementRelationshipDecisionInput(validationObservation); err != nil {
			return err
		}
		if entry.Observation.AssessmentID != input.AssessmentID {
			return errors.New("submission relationship observation assessment_id does not match the submission assessment")
		}
		if _, exists := seenRelationships[entry.Observation.Ref]; exists {
			return errors.New("submission relationship observation ref is duplicated")
		}
		seenRelationships[entry.Observation.Ref] = struct{}{}
	}
	seenRegistrations := map[string]struct{}{}
	for _, registration := range input.PredicateRegistrations {
		if registration.RelationshipRef == "" || registration.PredicateKey == "" {
			return errors.New("submission predicate registration ref and key are required")
		}
		if len([]rune(registration.PredicateKey)) > 128 {
			return errors.New("submission predicate registration key must be at most 128 characters")
		}
		if !contains(domain.EntityKinds(), registration.SubjectKind) || !contains(append(domain.EntityKinds(), domain.ValueTypes()...), registration.ObjectKind) {
			return errors.New("submission predicate registration endpoint kinds are unsupported")
		}
		if _, exists := seenRegistrations[registration.RelationshipRef]; exists {
			return errors.New("submission predicate registration relationship_ref is duplicated")
		}
		seenRegistrations[registration.RelationshipRef] = struct{}{}
	}
	return nil
}

func validateSubmissionAssessmentCommitScope(
	ctx context.Context,
	tx *gorm.DB,
	input CommitSubmissionAssessmentInput,
) error {
	var found int
	err := tx.WithContext(ctx).Raw(`
		SELECT 1
		FROM placement_assessments
		WHERE team_id = ?::uuid
		  AND assessment_id = ?::uuid
		  AND assessment_scope = 'submission'
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND placement_run_id = ?::uuid
	`, input.TeamID, input.AssessmentID, input.OwnerProfileID, input.IngestID, input.PlacementRunID).Scan(&found).Error
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrSubmissionAssessmentScopeMismatch
	}
	return nil
}

func lockSubmissionAssessmentRun(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
) error {
	var found int
	err := tx.WithContext(ctx).Raw(`
		SELECT 1
		FROM placement_runs
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status = 'processing'
		  AND worker_id = ?
		  AND attempts = ?
		  AND lease_until > clock_timestamp()
		FOR UPDATE
	`, scope.TeamID, scope.OwnerProfileID, scope.IngestID, scope.PlacementRunID,
		scope.WorkerID, scope.ExpectedAttempts).Scan(&found).Error
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrPlacementLeaseLost
	}
	return nil
}

func loadLockedSubmissionAssessmentItems(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
) ([]submissionLockedItem, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT item.placement_item_id::text,
		       item.fragment_id::text,
		       item.evidence_index,
		       item.status,
		       item.category,
		       fragment.content,
		       fragment.content_hash,
		       fragment.authority,
		       COALESCE(fragment.source_id::text, ''),
		       COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(source.current_revision_id::text, ''),
		       EXISTS (
		           SELECT 1
		           FROM evidence_lifecycle_events AS lifecycle
		           WHERE lifecycle.team_id = fragment.team_id
		             AND lifecycle.target_fragment_id = fragment.fragment_id
		       ) AS retired
		FROM placement_items AS item
		JOIN evidence_fragments AS fragment
		  ON fragment.team_id = item.team_id
		 AND fragment.fragment_id = item.fragment_id
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		WHERE item.team_id = ?::uuid
		  AND item.owner_profile_id = ?::uuid
		  AND item.ingest_id = ?::uuid
		  AND item.placement_run_id = ?::uuid
		ORDER BY item.evidence_index ASC, item.placement_item_id ASC
		FOR UPDATE OF item
	`, scope.TeamID, scope.OwnerProfileID, scope.IngestID, scope.PlacementRunID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []submissionLockedItem{}
	for rows.Next() {
		item := submissionLockedItem{}
		var currentRevisionID string
		var retired bool
		if err := rows.Scan(
			&item.PlacementItemID,
			&item.FragmentID,
			&item.EvidenceIndex,
			&item.Status,
			&item.Category,
			&item.Fragment.Content,
			&item.Fragment.ContentHash,
			&item.Fragment.Authority,
			&item.Fragment.SourceID,
			&item.Fragment.SourceRevisionID,
			&currentRevisionID,
			&retired,
		); err != nil {
			return nil, err
		}
		item.Fragment.FragmentID = item.FragmentID
		item.Fragment.EvidenceIndex = item.EvidenceIndex
		item.SourceStale = retired || (item.Fragment.SourceRevisionID != "" && currentRevisionID != "" && item.Fragment.SourceRevisionID != currentRevisionID)
		if item.Status != string(domain.PlacementRunQueued) && item.Status != string(domain.PlacementRunProcessing) {
			return nil, ErrPlacementLeaseLost
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrPlacementLeaseLost
	}
	return items, nil
}

func validateSubmissionCommitItems(
	expected []SubmissionAssessmentItemInput,
	actual []submissionLockedItem,
) error {
	if len(expected) != len(actual) {
		return ErrPlacementLeaseLost
	}
	byID := make(map[string]SubmissionAssessmentItemInput, len(expected))
	for _, item := range expected {
		byID[item.PlacementItemID] = item
	}
	for _, item := range actual {
		expectedItem, ok := byID[item.PlacementItemID]
		if !ok || expectedItem.FragmentID != item.FragmentID {
			return ErrPlacementLeaseLost
		}
	}
	return nil
}

func resolveSubmissionPredicates(
	ctx context.Context,
	tx *gorm.DB,
	input *CommitSubmissionAssessmentInput,
) error {
	registrations := make(map[string]SubmissionPredicateRegistrationInput, len(input.PredicateRegistrations))
	for _, registration := range input.PredicateRegistrations {
		registrations[registration.RelationshipRef] = registration
	}
	seen := make(map[string]struct{}, len(input.RelationshipObservations))
	for index := range input.RelationshipObservations {
		observation := &input.RelationshipObservations[index].Observation
		if _, exists := seen[observation.Ref]; exists {
			return errors.New("submission relationship observation ref is duplicated")
		}
		seen[observation.Ref] = struct{}{}
		registration, needsRegistration := registrations[observation.Ref]
		if !needsRegistration {
			if observation.PredicateKey == "" || observation.PredicateVersion < 1 {
				return ErrSubmissionAssessmentNonPromotable
			}
			continue
		}
		if observation.PredicateKey != "" {
			return errors.New("submission predicate registration must not include a provider-selected predicate")
		}
		resolved, action, err := resolveSubmissionPredicateRegistration(ctx, tx, input, registration)
		if err != nil {
			return err
		}
		observation.PredicateKey = resolved.PredicateKey
		observation.PredicateVersion = resolved.Version
		if err := insertSubmissionPredicateRegistrationEvent(ctx, tx, input, registration.RelationshipRef, action, resolved); err != nil {
			return err
		}
	}
	for ref := range registrations {
		if _, exists := seen[ref]; !exists {
			return errors.New("submission predicate registration has no relationship observation")
		}
	}
	return nil
}

func resolveSubmissionPredicateRegistration(
	ctx context.Context,
	tx *gorm.DB,
	input *CommitSubmissionAssessmentInput,
	registration SubmissionPredicateRegistrationInput,
) (SemanticReviewPredicateCandidate, string, error) {
	requestedKey := strings.TrimSpace(registration.PredicateKey)
	canonicalKey := canonicalGeneratedPredicateKey(requestedKey)
	if err := tx.WithContext(ctx).Exec(
		`SELECT pg_advisory_xact_lock(hashtext(?))`,
		input.TeamID+":"+canonicalKey,
	).Error; err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	loaded, err := loadLatestSubmissionPredicate(ctx, tx, input.TeamID, requestedKey, canonicalKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	if loaded != nil {
		if loaded.LifecycleState != string(domain.PredicateLifecycleActive) ||
			!placementPredicateKindAllowed(loaded.AllowedSubjectKinds, registration.SubjectKind) ||
			!placementPredicateKindAllowed(loaded.AllowedObjectKinds, registration.ObjectKind) {
			return SemanticReviewPredicateCandidate{}, "", ErrSubmissionPredicateRegistrationHeld
		}
		return *loaded, "reused", nil
	}
	metadata, err := marshalJSON(map[string]any{
		"source":           "submission_assessment_registration",
		"placement_run_id": input.PlacementRunID,
		"assessment_id":    input.AssessmentID,
		"relationship_ref": registration.RelationshipRef,
		"predicate_policy": domain.PredicatePolicyVersion,
	})
	if err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata
		) VALUES (
		    ?::uuid, ?, 1, ARRAY[]::text[], ?::text[], ?::text[],
		    'state', 'many', 'active', 'submission_registration', ?::jsonb
		)
		RETURNING predicate_key, version, aliases, allowed_subject_kinds,
		          allowed_object_kinds, relationship_kind, current_cardinality,
		          lifecycle_state
	`, input.TeamID, canonicalKey,
		pq.Array([]string{registration.SubjectKind}),
		pq.Array([]string{registration.ObjectKind}),
		string(metadata)).Rows()
	if err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return SemanticReviewPredicateCandidate{}, "", err
		}
		return SemanticReviewPredicateCandidate{}, "", sql.ErrNoRows
	}
	created, err := scanSubmissionPredicateCandidate(rows)
	if err != nil {
		return SemanticReviewPredicateCandidate{}, "", err
	}
	return created, "created", rows.Err()
}

func loadLatestSubmissionPredicate(
	ctx context.Context,
	tx *gorm.DB,
	teamID, requestedKey, canonicalKey string,
) (*SemanticReviewPredicateCandidate, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, aliases, allowed_subject_kinds,
		       allowed_object_kinds, relationship_kind, current_cardinality,
		       lifecycle_state
		FROM (
			SELECT predicate_key, version, aliases, allowed_subject_kinds,
			       allowed_object_kinds, relationship_kind, current_cardinality,
			       lifecycle_state,
			       row_number() OVER (
			           PARTITION BY predicate_key
			           ORDER BY version DESC
			       ) AS version_rank
			FROM team_predicate_definitions
			WHERE team_id = ?::uuid
		) AS latest
		WHERE version_rank = 1
		  AND (predicate_key = ? OR predicate_key = ? OR ? = ANY(aliases) OR ? = ANY(aliases))
		ORDER BY CASE WHEN predicate_key = ? THEN 0
		              WHEN predicate_key = ? THEN 1
		              ELSE 2 END,
		         predicate_key ASC
		LIMIT 2
	`, teamID, requestedKey, canonicalKey, requestedKey, canonicalKey, requestedKey, canonicalKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	candidate, err := scanSubmissionPredicateCandidate(rows)
	if err != nil {
		return nil, err
	}
	if candidate.PredicateKey == canonicalKey || candidate.PredicateKey == requestedKey {
		return &candidate, rows.Err()
	}
	if rows.Next() {
		return nil, ErrSubmissionPredicateRegistrationHeld
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &candidate, rows.Err()
}

func scanSubmissionPredicateCandidate(rows *sql.Rows) (SemanticReviewPredicateCandidate, error) {
	candidate := SemanticReviewPredicateCandidate{}
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
		return SemanticReviewPredicateCandidate{}, err
	}
	candidate.Aliases = []string(aliases)
	candidate.AllowedSubjectKinds = []string(subjectKinds)
	candidate.AllowedObjectKinds = []string(objectKinds)
	return candidate, nil
}

func insertSubmissionPredicateRegistrationEvent(
	ctx context.Context,
	tx *gorm.DB,
	input *CommitSubmissionAssessmentInput,
	relationshipRef, action string,
	predicate SemanticReviewPredicateCandidate,
) error {
	metadata, err := marshalJSON(map[string]any{
		"source":        "submission_assessment",
		"assessment_id": input.AssessmentID,
	})
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO predicate_registration_events (
		    team_id, placement_run_id, assessment_id, owner_profile_id,
		    relationship_ref, registration_action, predicate_key,
		    predicate_version, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    ?, ?, ?, ?, ?::jsonb
		)
	`, input.TeamID, input.PlacementRunID, input.AssessmentID, input.OwnerProfileID,
		relationshipRef, action, predicate.PredicateKey, predicate.Version, string(metadata)).Error
}
