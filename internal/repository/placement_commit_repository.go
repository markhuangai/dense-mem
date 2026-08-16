package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrPlacementLeaseLost          = errors.New("placement lease lost")
	ErrPlacementStaleSource        = errors.New("placement stale source")
	ErrConflictContextStale        = errors.New("conflict context stale")
	errPlacementUnresolvedEndpoint = errors.New("placement unresolved relationship endpoint")
	errPlacementPredicateReview    = errors.New("placement predicate requires review")
)

type PlacementCommitRepository interface {
	CommitPlacementSemanticResult(ctx context.Context, input CommitPlacementSemanticInput) (*CommitPlacementSemanticResult, error)
	CompletePlacementReviewResult(ctx context.Context, input CompletePlacementReviewInput) (*CompletePlacementReviewResult, error)
	RequeuePlacementReviewResult(ctx context.Context, input RequeuePlacementReviewInput) (*RequeuePlacementReviewResult, error)
}

var _ PlacementCommitRepository = (*LedgerRepositoryImpl)(nil)

func (r *LedgerRepositoryImpl) CommitPlacementSemanticResult(
	ctx context.Context,
	input CommitPlacementSemanticInput,
) (*CommitPlacementSemanticResult, error) {
	input = normalizeCommitPlacementSemanticInput(input)
	if err := validateCommitPlacementSemanticInput(input); err != nil {
		return nil, err
	}
	result := &CommitPlacementSemanticResult{}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockPlacementRunForCommit(ctx, tx, input); err != nil {
			return err
		}
		if err := lockPlacementItemForCommit(ctx, tx, input); err != nil {
			return err
		}
		placementFragmentID, err := loadPlacementItemFragmentID(ctx, tx, input)
		if err != nil {
			return err
		}
		if err := lockEvidenceLifecycleTargetIDs(ctx, tx, input.TeamID, []string{placementFragmentID}); err != nil {
			return err
		}
		if err := ensurePlacementItemCurrent(ctx, tx, input); err != nil {
			if errors.Is(err, ErrPlacementStaleSource) {
				outcomeID, outcomeErr := appendSupersededPlacementOutcome(ctx, tx, input)
				if outcomeErr != nil {
					return outcomeErr
				}
				firstDisposition, finishErr := finishPlacementRunIfTerminal(ctx, tx, input, string(domain.PlacementRunFailed))
				if finishErr != nil {
					return finishErr
				}
				result.FirstDisposition = firstDisposition
				result.Status = "superseded"
				result.OutcomeID = outcomeID
				return nil
			}
			return err
		}
		deletionOnly, err := isConflictResolutionDeletionOnlyFragment(ctx, tx, input.TeamID, placementFragmentID)
		if err != nil {
			return err
		}
		if !deletionOnly {
			if err := ensureRelationshipConflictContextsCurrent(ctx, tx, input); err != nil {
				return err
			}
		}
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if placementEvidenceSearchableStatus(input.Status) && !deletionOnly {
			document, err := upsertPlacementItemEvidenceSearchDocument(
				ctx,
				tx,
				input,
				placementFragmentID,
				r.embeddingJobMaxAttempts,
			)
			if err != nil {
				return err
			}
			appendPlacementSearchDocument(result, document)
		}
		if !deletionOnly {
			entitiesByRef := make(map[string]string, len(input.EntityResolutions))
			for _, resolution := range input.EntityResolutions {
				resolutionID, entityID, err := insertPlacementEntityResolution(ctx, tx, input, resolution)
				if err != nil {
					return err
				}
				result.EntityResolutionIDs = append(result.EntityResolutionIDs, resolutionID)
				if entityID != "" {
					entitiesByRef[resolution.MentionRef] = entityID
				}
				if resolution.Action == string(domain.EntityResolutionAmbiguous) {
					taskID, err := insertEntityReviewTask(ctx, tx, input, resolution, resolutionID)
					if err != nil {
						return err
					}
					appendPlacementReviewTaskID(result, taskID)
				}
			}
			for _, observation := range input.RelationshipObservations {
				decision, err := relationshipDecisionFromPlacementObservation(ctx, tx, input, observation, entitiesByRef)
				if err != nil {
					if errors.Is(err, errPlacementPredicateReview) {
						review, reviewErr := insertRelationshipPredicateReview(ctx, tx, input, observation, err.Error())
						if reviewErr != nil {
							return reviewErr
						}
						appendPlacementRelationshipResult(result, review)
						appendPlacementReviewTaskID(result, review.ReviewTaskID)
						continue
					}
					if !errors.Is(err, errPlacementUnresolvedEndpoint) {
						return err
					}
					review, reviewErr := insertRelationshipDependencyReview(ctx, tx, input, observation, err.Error())
					if reviewErr != nil {
						return reviewErr
					}
					appendPlacementRelationshipResult(result, review)
					appendPlacementReviewTaskID(result, review.ReviewTaskID)
					continue
				}
				if observation.ConflictContext != nil {
					if err := requireRelationshipConflictContextMatchesDecision(ctx, tx, input.TeamID, *observation.ConflictContext, decision); err != nil {
						return err
					}
				}
				if err := applyPlacementRelationshipDecision(
					ctx,
					tx,
					input,
					decision,
					observation.CorrectionTarget,
					observation.ConflictContext,
					placementFragmentID,
					r.embeddingJobMaxAttempts,
					ConflictRuntimeConfig{
						ReviewTTLDays: r.conflictReviewTTLDays,
						Timezone:      r.conflictReviewTimezone,
					},
					result,
				); err != nil {
					return err
				}
			}
			for _, review := range input.RelationshipReviews {
				recorded, err := insertRelationshipReview(ctx, tx, input, review)
				if err != nil {
					return err
				}
				appendPlacementRelationshipResult(result, recorded)
				appendPlacementReviewTaskID(result, recorded.ReviewTaskID)
			}
			for _, decision := range input.RelationshipDecisions {
				if err := applyPlacementRelationshipDecision(
					ctx,
					tx,
					input,
					withPlacementDecisionScope(input, decision),
					nil,
					nil,
					placementFragmentID,
					r.embeddingJobMaxAttempts,
					ConflictRuntimeConfig{
						ReviewTTLDays: r.conflictReviewTTLDays,
						Timezone:      r.conflictReviewTimezone,
					},
					result,
				); err != nil {
					return err
				}
			}
		}
		payload := placementCommitPayload(input.Payload, result)
		if deletionOnly {
			payload["conflict_resolution_deletion_only"] = true
			payload["semantic_projection"] = "not_allowed"
			input.Category = "candidate"
		}
		itemStatus := string(domain.PlacementRunCompleted)
		runStatus := string(domain.PlacementRunCompleted)
		if len(result.ReviewTaskIDs) > 0 {
			itemStatus = string(domain.PlacementRunAwaitingReview)
			runStatus = string(domain.PlacementRunAwaitingReview)
			input.Category = "candidate"
		}
		outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementRunID:     input.PlacementRunID,
			PlacementItemID:    input.PlacementItemID,
			OutcomeKind:        input.OutcomeKind,
			Status:             input.Status,
			Payload:            payload,
			UpdateItemStatus:   itemStatus,
			UpdateItemCategory: input.Category,
		})
		if err != nil {
			return err
		}
		if err := updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementItemID:    input.PlacementItemID,
			UpdateItemStatus:   itemStatus,
			UpdateItemCategory: input.Category,
			Payload:            payload,
		}); err != nil {
			return err
		}
		firstDisposition, err := finishPlacementRunIfTerminal(ctx, tx, input, runStatus)
		if err != nil {
			return err
		}
		result.FirstDisposition = firstDisposition
		result.Status = input.Status
		result.OutcomeID = outcomeID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("placement commit: %w", err)
	}
	return result, nil
}

func normalizeCommitPlacementSemanticInput(input CommitPlacementSemanticInput) CommitPlacementSemanticInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	input.Status = strings.TrimSpace(input.Status)
	input.Category = strings.TrimSpace(input.Category)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "semantic_commit"
	}
	if input.Status == "" {
		input.Status = string(domain.SemanticReviewAccepted)
	}
	if input.Category == "" {
		input.Category = "validated_claim"
	}
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > placementRetryMaxDelay {
		input.RetryAfter = placementRetryMaxDelay
	}
	for i := range input.EntityResolutions {
		resolution := &input.EntityResolutions[i]
		resolution.MentionRef = strings.TrimSpace(resolution.MentionRef)
		resolution.Action = strings.TrimSpace(resolution.Action)
		resolution.EntityID = strings.TrimSpace(resolution.EntityID)
		resolution.EntityKind = strings.TrimSpace(resolution.EntityKind)
		resolution.CanonicalName = strings.TrimSpace(resolution.CanonicalName)
		resolution.FragmentID = strings.TrimSpace(resolution.FragmentID)
		resolution.AssessmentID = strings.TrimSpace(resolution.AssessmentID)
		resolution.SemanticReviewKind = strings.TrimSpace(resolution.SemanticReviewKind)
		resolution.ReviewQuestion = strings.TrimSpace(resolution.ReviewQuestion)
		resolution.ReviewGuidance = strings.TrimSpace(resolution.ReviewGuidance)
		if resolution.EntityKind == "" {
			resolution.EntityKind = string(domain.EntityKindOther)
		}
	}
	for i := range input.RelationshipObservations {
		observation := &input.RelationshipObservations[i]
		*observation = normalizePlacementRelationshipDecisionInput(*observation)
	}
	for i := range input.RelationshipReviews {
		review := &input.RelationshipReviews[i]
		review.Ref = strings.TrimSpace(review.Ref)
		review.SubjectRef = strings.TrimSpace(review.SubjectRef)
		review.OriginalPredicate = strings.TrimSpace(review.OriginalPredicate)
		review.ObjectRef = strings.TrimSpace(review.ObjectRef)
		review.Polarity = strings.TrimSpace(review.Polarity)
		review.EvidenceVerdict = strings.TrimSpace(review.EvidenceVerdict)
		review.Rationale = strings.TrimSpace(review.Rationale)
		review.Model = strings.TrimSpace(review.Model)
		review.ResponseHash = strings.TrimSpace(review.ResponseHash)
		review.Reason = strings.TrimSpace(review.Reason)
		review.AssessmentID = strings.TrimSpace(review.AssessmentID)
		review.AssessmentPolicyVersion = strings.TrimSpace(review.AssessmentPolicyVersion)
		review.GateResult = strings.TrimSpace(review.GateResult)
		review.SemanticReviewKind = strings.TrimSpace(review.SemanticReviewKind)
		review.ReviewQuestion = strings.TrimSpace(review.ReviewQuestion)
		review.ReviewGuidance = strings.TrimSpace(review.ReviewGuidance)
		if review.Polarity == "" {
			review.Polarity = "+"
		}
		if review.Reason == "" {
			review.Reason = "relationship_needs_review"
		}
		if review.ObjectValue != nil {
			value := normalizePlacementValueInput(*review.ObjectValue)
			review.ObjectValue = &value
		}
		review.Support, review.Supports = normalizeEvidenceSupports(review.Support, review.Supports)
		if review.CorrectionTarget != nil {
			target := *review.CorrectionTarget
			target.RelationshipID = strings.TrimSpace(target.RelationshipID)
			review.CorrectionTarget = &target
		}
		if review.ConflictContext != nil {
			context := *review.ConflictContext
			context.ConflictID = strings.TrimSpace(context.ConflictID)
			review.ConflictContext = &context
		}
	}
	return input
}

func validateCommitPlacementSemanticInput(input CommitPlacementSemanticInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"ingest_id":         input.IngestID,
		"placement_run_id":  input.PlacementRunID,
		"placement_item_id": input.PlacementItemID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.ExpectedAttempts < 1 {
		return errors.New("expected_attempts must be greater than zero")
	}
	if input.Status == "" {
		return errors.New("status is required")
	}
	if input.Category != "candidate" && input.Category != "validated_claim" && input.Category != "fact" {
		return fmt.Errorf("unsupported placement category %q", input.Category)
	}
	for _, resolution := range input.EntityResolutions {
		if err := validatePlacementEntityResolutionInput(resolution); err != nil {
			return err
		}
	}
	for _, observation := range input.RelationshipObservations {
		if err := validatePlacementRelationshipDecisionInput(observation); err != nil {
			return err
		}
	}
	for _, review := range input.RelationshipReviews {
		if err := validatePlacementRelationshipReviewInput(review); err != nil {
			return err
		}
	}
	for _, decision := range input.RelationshipDecisions {
		scoped := withPlacementDecisionScope(input, decision)
		if err := validateApplyRelationshipDecisionInput(normalizeApplyRelationshipDecisionInput(scoped)); err != nil {
			return err
		}
	}
	return nil
}

func placementCommitNeedsPlacementFragmentID(input CommitPlacementSemanticInput) bool {
	if placementEvidenceSearchableStatus(input.Status) {
		return true
	}
	for _, observation := range input.RelationshipObservations {
		for _, support := range relationshipEvidenceSupports(observation.Support, observation.Supports) {
			if strings.TrimSpace(support.FragmentID) == "" {
				continue
			}
			return true
		}
	}
	for _, decision := range input.RelationshipDecisions {
		for _, support := range relationshipEvidenceSupports(decision.Support, decision.Supports) {
			if strings.TrimSpace(support.FragmentID) == "" {
				continue
			}
			return true
		}
	}
	return false
}

func validatePlacementEntityResolutionInput(input PlacementEntityResolutionInput) error {
	if input.AssessmentID != "" {
		if _, err := uuid.Parse(input.AssessmentID); err != nil {
			return fmt.Errorf("entity resolution assessment_id is invalid: %w", err)
		}
	}
	if err := validateSemanticReviewDetails(input.AssessmentID, input.SemanticReviewKind, input.ReviewQuestion, input.ReviewOptions, input.ReviewGuidance); err != nil {
		return err
	}
	if input.MentionRef == "" {
		return errors.New("entity resolution mention_ref is required")
	}
	switch input.Action {
	case string(domain.EntityResolutionReuse):
		if _, err := uuid.Parse(input.EntityID); err != nil {
			return fmt.Errorf("entity resolution entity_id is required: %w", err)
		}
	case string(domain.EntityResolutionCreate):
		if input.CanonicalName == "" {
			return errors.New("entity resolution canonical_name is required")
		}
		if !contains(domain.EntityKinds(), input.EntityKind) {
			return fmt.Errorf("unsupported entity_kind %q", input.EntityKind)
		}
	case string(domain.EntityResolutionAmbiguous):
		return nil
	default:
		return fmt.Errorf("unsupported entity resolution action %q", input.Action)
	}
	return nil
}

func normalizePlacementRelationshipDecisionInput(input PlacementRelationshipDecisionInput) PlacementRelationshipDecisionInput {
	input.Ref = strings.TrimSpace(input.Ref)
	input.SubjectRef = strings.TrimSpace(input.SubjectRef)
	input.OriginalPredicate = strings.TrimSpace(input.OriginalPredicate)
	input.PredicateKey = strings.TrimSpace(input.PredicateKey)
	input.ObjectRef = strings.TrimSpace(input.ObjectRef)
	input.Polarity = strings.TrimSpace(input.Polarity)
	input.ScopeKey = strings.TrimSpace(input.ScopeKey)
	input.EvidenceVerdict = strings.TrimSpace(input.EvidenceVerdict)
	input.Rationale = strings.TrimSpace(input.Rationale)
	input.Model = strings.TrimSpace(input.Model)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	input.AssessmentPolicyVersion = strings.TrimSpace(input.AssessmentPolicyVersion)
	input.GateResult = strings.TrimSpace(input.GateResult)
	input.SemanticReviewKind = strings.TrimSpace(input.SemanticReviewKind)
	input.ReviewQuestion = strings.TrimSpace(input.ReviewQuestion)
	input.ReviewGuidance = strings.TrimSpace(input.ReviewGuidance)
	if input.PredicateCandidate != nil {
		candidate := *input.PredicateCandidate
		candidate.PredicateKey = strings.TrimSpace(candidate.PredicateKey)
		candidate.RelationshipKind = strings.TrimSpace(candidate.RelationshipKind)
		input.PredicateCandidate = &candidate
	}
	if input.PredicateVersion == 0 {
		input.PredicateVersion = 1
	}
	if input.OriginalPredicate == "" {
		input.OriginalPredicate = input.PredicateKey
	}
	if input.Polarity == "" {
		input.Polarity = "+"
	}
	if input.EvidenceVerdict == "" {
		input.EvidenceVerdict = string(domain.VerificationEntailed)
	}
	if input.ObjectValue != nil {
		value := normalizePlacementValueInput(*input.ObjectValue)
		input.ObjectValue = &value
	}
	input.Support, input.Supports = normalizeEvidenceSupports(input.Support, input.Supports)
	if input.CorrectionTarget != nil {
		target := *input.CorrectionTarget
		target.RelationshipID = strings.TrimSpace(target.RelationshipID)
		input.CorrectionTarget = &target
	}
	if input.ConflictContext != nil {
		context := *input.ConflictContext
		context.ConflictID = strings.TrimSpace(context.ConflictID)
		input.ConflictContext = &context
	}
	return input
}

func validatePlacementRelationshipDecisionInput(input PlacementRelationshipDecisionInput) error {
	if err := validateAssessmentDecisionAudit(input.AssessmentID, input.AssessmentPolicyVersion, input.ThresholdUsed, input.GateResult, input.SuppressSupport); err != nil {
		return err
	}
	if err := validateSemanticReviewDetails(input.AssessmentID, input.SemanticReviewKind, input.ReviewQuestion, input.ReviewOptions, input.ReviewGuidance); err != nil {
		return err
	}
	if input.SubjectRef == "" {
		return errors.New("relationship observation subject_ref is required")
	}
	if input.PredicateKey == "" {
		return errors.New("relationship observation predicate_key is required")
	}
	if input.PredicateVersion < 1 {
		return errors.New("relationship observation predicate_version must be greater than zero")
	}
	if input.PredicateCandidate != nil {
		if input.PredicateCandidate.PredicateKey != input.PredicateKey {
			return errors.New("relationship observation predicate candidate must match predicate_key")
		}
		if input.PredicateCandidate.PredicateVersion != input.PredicateVersion {
			return errors.New("relationship observation predicate candidate must match predicate_version")
		}
		if !contains(domain.RelationshipKinds(), input.PredicateCandidate.RelationshipKind) {
			return fmt.Errorf(
				"unsupported relationship observation predicate relationship_kind %q",
				input.PredicateCandidate.RelationshipKind,
			)
		}
	}
	if (input.ObjectRef == "") == (input.ObjectValue == nil) {
		return errors.New("relationship observation requires exactly one object endpoint")
	}
	if input.ObjectValue != nil {
		if err := validatePlacementValueInput(*input.ObjectValue); err != nil {
			return err
		}
	}
	if input.Polarity != "+" && input.Polarity != "-" {
		return fmt.Errorf("unsupported relationship observation polarity %q", input.Polarity)
	}
	if !contains(domain.VerificationVerdicts(), input.EvidenceVerdict) {
		return fmt.Errorf("unsupported relationship observation evidence_verdict %q", input.EvidenceVerdict)
	}
	if input.Confidence != nil && (*input.Confidence < 0 || *input.Confidence > 1) {
		return errors.New("relationship observation confidence must be between 0 and 1")
	}
	if input.ValidFrom != nil && input.ValidTo != nil && input.ValidTo.Before(*input.ValidFrom) {
		return errors.New("relationship observation valid_to must be greater than or equal to valid_from")
	}
	if input.EvidenceVerdict == string(domain.VerificationEntailed) {
		if len(relationshipEvidenceSupports(input.Support, input.Supports)) == 0 {
			return errors.New("entailed relationship observations require support")
		}
	}
	if err := validateRelationshipEvidenceSupports(input.Support, input.Supports); err != nil {
		return err
	}
	if input.CorrectionTarget != nil {
		if err := validatePlacementCorrectionTargetInput(*input.CorrectionTarget); err != nil {
			return err
		}
	}
	if input.ConflictContext != nil {
		if err := validatePlacementConflictContextInput(*input.ConflictContext); err != nil {
			return err
		}
	}
	return nil
}

func validatePlacementCorrectionTargetInput(input PlacementCorrectionTargetInput) error {
	if _, err := uuid.Parse(input.RelationshipID); err != nil {
		return fmt.Errorf("correction target relationship_id is required: %w", err)
	}
	if input.ExpectedVersion < 1 {
		return errors.New("correction target expected_version must be greater than zero")
	}
	return nil
}

func validatePlacementConflictContextInput(input PlacementConflictContextInput) error {
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return fmt.Errorf("conflict context conflict_id is required: %w", err)
	}
	if input.ExpectedVersion < 1 {
		return errors.New("conflict context expected_version must be greater than zero")
	}
	return nil
}

func normalizePlacementValueInput(input PlacementValueInput) PlacementValueInput {
	input.Ref = strings.TrimSpace(input.Ref)
	input.ValueType = strings.TrimSpace(input.ValueType)
	input.CanonicalValue = strings.TrimSpace(input.CanonicalValue)
	input.Unit = strings.TrimSpace(input.Unit)
	input.Display = strings.TrimSpace(input.Display)
	if input.Display == "" {
		input.Display = input.CanonicalValue
	}
	if input.NormalizationVersion == 0 {
		input.NormalizationVersion = 1
	}
	return input
}

func validatePlacementValueInput(input PlacementValueInput) error {
	if !contains(domain.ValueTypes(), input.ValueType) {
		return fmt.Errorf("unsupported placement value_type %q", input.ValueType)
	}
	if input.CanonicalValue == "" {
		return errors.New("placement value canonical_value is required")
	}
	if input.NormalizationVersion < 1 {
		return errors.New("placement value normalization_version must be greater than zero")
	}
	return nil
}

func lockPlacementRunForCommit(ctx context.Context, tx *gorm.DB, input CommitPlacementSemanticInput) error {
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
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID,
		input.WorkerID, input.ExpectedAttempts).Scan(&found).Error
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrPlacementLeaseLost
	}
	return nil
}

func lockPlacementItemForCommit(ctx context.Context, tx *gorm.DB, input CommitPlacementSemanticInput) error {
	var found int
	err := tx.WithContext(ctx).Raw(`
		SELECT 1
		FROM placement_items
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND placement_item_id = ?::uuid
		  AND status IN ('queued', 'processing')
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID,
		input.PlacementItemID).Scan(&found).Error
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrPlacementLeaseLost
	}
	return nil
}

func ensurePlacementItemCurrent(ctx context.Context, tx *gorm.DB, input CommitPlacementSemanticInput) error {
	var sourceRevisionID, currentRevisionID sql.NullString
	var retired bool
	err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(source.current_revision_id::text, ''),
		       EXISTS (
		           SELECT 1
		           FROM evidence_lifecycle_events AS lifecycle
		           WHERE lifecycle.team_id = fragment.team_id
		             AND lifecycle.target_fragment_id = fragment.fragment_id
		       )
		FROM placement_items AS item
		JOIN evidence_fragments AS fragment
		  ON fragment.team_id = item.team_id
		 AND fragment.fragment_id = item.fragment_id
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		WHERE item.team_id = ?::uuid
		  AND item.owner_profile_id = ?::uuid
		  AND item.placement_item_id = ?::uuid
	`, input.TeamID, input.OwnerProfileID, input.PlacementItemID).Row().Scan(&sourceRevisionID, &currentRevisionID, &retired)
	if err != nil {
		return err
	}
	if retired || (sourceRevisionID.String != "" && currentRevisionID.String != "" && sourceRevisionID.String != currentRevisionID.String) {
		return ErrPlacementStaleSource
	}
	return nil
}

func appendSupersededPlacementOutcome(ctx context.Context, tx *gorm.DB, input CommitPlacementSemanticInput) (string, error) {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
		"status":           "superseded",
		"reason":           "evidence lifecycle or source revision changed before semantic commit",
	}
	outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
		TeamID:          input.TeamID,
		OwnerProfileID:  input.OwnerProfileID,
		PlacementRunID:  input.PlacementRunID,
		PlacementItemID: input.PlacementItemID,
		OutcomeKind:     "semantic_commit",
		Status:          "superseded",
		Payload:         payload,
	})
	if err != nil {
		return "", err
	}
	return outcomeID, updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
		TeamID:             input.TeamID,
		OwnerProfileID:     input.OwnerProfileID,
		PlacementItemID:    input.PlacementItemID,
		UpdateItemStatus:   "failed",
		UpdateItemCategory: "failed",
		Payload:            payload,
	})
}

func insertPlacementEntityResolution(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementEntityResolutionInput,
) (string, string, error) {
	entityID := input.EntityID
	if input.Action == string(domain.EntityResolutionCreate) {
		created, err := insertPlacementEntity(ctx, tx, commit, input)
		if err != nil {
			return "", "", err
		}
		entityID = created
	}
	verifierResult, err := marshalJSON(input.VerifierResult)
	if err != nil {
		return "", "", err
	}
	metadata, err := marshalJSON(input.Metadata)
	if err != nil {
		return "", "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_resolution_events (
		    team_id, ingest_id, placement_item_id, owner_profile_id, mention_ref,
		    action, entity_id, fragment_id, span_start, span_end, verifier_result, metadata,
		    assessment_id
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid,
		    NULLIF(?, '')::uuid, ?, ?, ?::jsonb, ?::jsonb, NULLIF(?, '')::uuid
		)
		RETURNING resolution_event_id::text
	`, commit.TeamID, commit.IngestID, commit.PlacementItemID, commit.OwnerProfileID,
		input.MentionRef, input.Action, entityID, input.FragmentID,
		intPointerArg(input.SpanStart), intPointerArg(input.SpanEnd),
		string(verifierResult), string(metadata), input.AssessmentID).Rows()
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", rows.Err()
	}
	var resolutionID string
	if err := rows.Scan(&resolutionID); err != nil {
		return "", "", err
	}
	return resolutionID, entityID, rows.Err()
}

func insertEntityReviewTask(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementEntityResolutionInput,
	resolutionID string,
) (string, error) {
	payload := map[string]any{
		"mention_ref":         input.MentionRef,
		"resolution_event_id": resolutionID,
		"action":              input.Action,
		"entity_kind":         input.EntityKind,
		"canonical_name":      input.CanonicalName,
		"reason":              "ambiguous_entity",
	}
	appendSemanticReviewPayload(payload, input.AssessmentID, input.SemanticReviewKind, input.ReviewQuestion, input.ReviewOptions, input.ReviewGuidance)
	payloadJSON, err := marshalJSON(payload)
	if err != nil {
		return "", err
	}
	dedupeKey := "identity:" + commit.PlacementItemID + ":" + input.MentionRef
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO review_tasks (
		    team_id, owner_profile_id, ingest_id, placement_item_id,
		    task_type, status, reason, payload, dedupe_key, assessment_id, expires_at, updated_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    'identity_needs_review', 'open', 'ambiguous_entity', ?::jsonb, ?,
		    NULLIF(?, '')::uuid,
		    CASE WHEN NULLIF(?, '') IS NULL THEN NULL ELSE now() + interval '7 days' END,
		    now()
		)
		ON CONFLICT (team_id, dedupe_key)
		WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged')
		DO UPDATE SET payload = EXCLUDED.payload,
		              assessment_id = COALESCE(EXCLUDED.assessment_id, review_tasks.assessment_id),
		              expires_at = COALESCE(EXCLUDED.expires_at, review_tasks.expires_at),
		              version = review_tasks.version + 1,
		              updated_at = now()
		RETURNING review_task_id::text
	`, commit.TeamID, commit.OwnerProfileID, commit.IngestID, commit.PlacementItemID,
		string(payloadJSON), dedupeKey, input.AssessmentID, input.AssessmentID).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var taskID string
	if err := rows.Scan(&taskID); err != nil {
		return "", err
	}
	return taskID, rows.Err()
}

func relationshipDecisionFromPlacementObservation(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementRelationshipDecisionInput,
	entitiesByRef map[string]string,
) (ApplyRelationshipDecisionInput, error) {
	subjectID := entitiesByRef[input.SubjectRef]
	if subjectID == "" {
		return ApplyRelationshipDecisionInput{}, fmt.Errorf("%w: relationship observation %q references subject_ref %q", errPlacementUnresolvedEndpoint, input.Ref, input.SubjectRef)
	}
	decision := ApplyRelationshipDecisionInput{
		TeamID:                  commit.TeamID,
		OwnerProfileID:          commit.OwnerProfileID,
		IngestID:                commit.IngestID,
		PlacementItemID:         commit.PlacementItemID,
		ProposalRef:             input.Ref,
		SubjectRef:              input.SubjectRef,
		SubjectEntityID:         subjectID,
		OriginalPredicate:       input.OriginalPredicate,
		PredicateKey:            input.PredicateKey,
		PredicateVersion:        input.PredicateVersion,
		Polarity:                input.Polarity,
		ScopeKey:                input.ScopeKey,
		ValidFrom:               input.ValidFrom,
		ValidTo:                 input.ValidTo,
		EvidenceVerdict:         input.EvidenceVerdict,
		PromoteToFact:           input.PromoteToFact,
		Confidence:              input.Confidence,
		Rationale:               input.Rationale,
		Model:                   input.Model,
		ResponseHash:            input.ResponseHash,
		Support:                 input.Support,
		Supports:                input.Supports,
		ObservationMetadata:     input.ObservationMetadata,
		RelationshipMetadata:    input.RelationshipMetadata,
		AssessmentID:            input.AssessmentID,
		AssessmentPolicyVersion: input.AssessmentPolicyVersion,
		ThresholdUsed:           input.ThresholdUsed,
		GateResult:              input.GateResult,
		SuppressSupport:         input.SuppressSupport,
		SemanticReviewKind:      input.SemanticReviewKind,
		ReviewQuestion:          input.ReviewQuestion,
		ReviewOptions:           input.ReviewOptions,
		ReviewGuidance:          input.ReviewGuidance,
	}
	if input.ObjectValue != nil {
		value, err := upsertPlacementValue(ctx, tx, commit, *input.ObjectValue)
		if err != nil {
			return ApplyRelationshipDecisionInput{}, err
		}
		decision.ObjectRef = input.ObjectValue.Ref
		if decision.ObjectRef == "" {
			decision.ObjectRef = value.ValueID
		}
		decision.ObjectValueID = value.ValueID
	} else {
		objectID := entitiesByRef[input.ObjectRef]
		if objectID == "" {
			return ApplyRelationshipDecisionInput{}, fmt.Errorf("%w: relationship observation %q references object_ref %q", errPlacementUnresolvedEndpoint, input.Ref, input.ObjectRef)
		}
		decision.ObjectRef = input.ObjectRef
		decision.ObjectEntityID = objectID
	}
	if input.PredicateCandidate != nil {
		resolved, err := resolvePlacementPredicateCandidate(ctx, tx, decision, *input.PredicateCandidate)
		if err != nil {
			return ApplyRelationshipDecisionInput{}, err
		}
		decision = resolved
	}
	decision = normalizeApplyRelationshipDecisionInput(decision)
	if err := validateApplyRelationshipDecisionInput(decision); err != nil {
		return ApplyRelationshipDecisionInput{}, err
	}
	return decision, nil
}

func upsertPlacementValue(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementValueInput,
) (*ValueRecord, error) {
	metadata, err := marshalJSON(input.Metadata)
	if err != nil {
		return nil, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
			INSERT INTO value_records (
			    team_id, value_type, canonical_value, unit, display,
			    normalization_version, metadata
			) VALUES (
			    ?::uuid, ?, ?, NULLIF(?, ''), ?, ?, ?::jsonb
			)
			ON CONFLICT ON CONSTRAINT value_records_canonical_unique
			DO NOTHING
			RETURNING team_id::text, value_id::text, value_type, canonical_value,
			          COALESCE(unit, ''), display, normalization_version, false AS existing
		)
		SELECT * FROM inserted
		UNION ALL
		SELECT team_id::text, value_id::text, value_type, canonical_value,
		       COALESCE(unit, ''), display, normalization_version, true AS existing
		FROM value_records
		WHERE team_id = ?::uuid
		  AND value_type = ?
		  AND canonical_value = ?
		  AND unit IS NOT DISTINCT FROM NULLIF(?, '')
		  AND normalization_version = ?
		LIMIT 1
	`, commit.TeamID, input.ValueType, input.CanonicalValue, input.Unit, input.Display,
		input.NormalizationVersion, string(metadata),
		commit.TeamID, input.ValueType, input.CanonicalValue, input.Unit,
		input.NormalizationVersion).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	loaded := ValueRecord{}
	if err := rows.Scan(&loaded.TeamID, &loaded.ValueID, &loaded.ValueType, &loaded.CanonicalValue,
		&loaded.Unit, &loaded.Display, &loaded.NormalizationVersion, &loaded.Existing); err != nil {
		return nil, err
	}
	return &loaded, rows.Err()
}
