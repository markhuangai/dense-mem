package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrV2PlacementLeaseLost          = errors.New("v2 placement lease lost")
	ErrV2PlacementStaleSource        = errors.New("v2 placement stale source")
	errV2PlacementUnresolvedEndpoint = errors.New("v2 placement unresolved relationship endpoint")
)

type V2PlacementCommitRepository interface {
	CommitPlacementSemanticResult(ctx context.Context, input V2CommitPlacementSemanticInput) (*V2CommitPlacementSemanticResult, error)
	CompletePlacementReviewResult(ctx context.Context, input V2CompletePlacementReviewInput) (*V2CompletePlacementReviewResult, error)
	RequeuePlacementReviewResult(ctx context.Context, input V2RequeuePlacementReviewInput) (*V2RequeuePlacementReviewResult, error)
}

type V2CommitPlacementSemanticInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	MigrationRunID   string
	MigrationEpoch   int
	OutcomeKind      string
	Status           string
	Category         string
	Payload          map[string]any

	EntityResolutions        []V2PlacementEntityResolutionInput
	RelationshipObservations []V2PlacementRelationshipDecisionInput
	RelationshipReviews      []V2PlacementRelationshipReviewInput
	RelationshipDecisions    []V2ApplyRelationshipDecisionInput
}

type V2PlacementEntityResolutionInput struct {
	MentionRef      string
	Action          string
	EntityID        string
	EntityKind      string
	CanonicalName   string
	FragmentID      string
	SpanStart       *int
	SpanEnd         *int
	IdentityContext map[string]any
	VerifierResult  map[string]any
	Metadata        map[string]any
}

type V2PlacementRelationshipDecisionInput struct {
	Ref                  string
	SubjectRef           string
	OriginalPredicate    string
	PredicateKey         string
	PredicateVersion     int
	ObjectRef            string
	ObjectValue          *V2PlacementValueInput
	Polarity             string
	ScopeKey             string
	ValidFrom            *time.Time
	ValidTo              *time.Time
	EvidenceVerdict      string
	PromoteToFact        bool
	Confidence           *float64
	Rationale            string
	Model                string
	ResponseHash         string
	Support              *V2EvidenceSupportInput
	CorrectionTarget     *V2PlacementCorrectionTargetInput
	ObservationMetadata  map[string]any
	RelationshipMetadata map[string]any
}

type V2PlacementRelationshipReviewInput struct {
	Ref               string
	SubjectRef        string
	OriginalPredicate string
	ObjectRef         string
	ObjectValue       *V2PlacementValueInput
	Polarity          string
	EvidenceVerdict   string
	Reason            string
	Payload           map[string]any
}

type V2PlacementCorrectionTargetInput struct {
	RelationshipID  string
	ExpectedVersion int
}

type V2PlacementValueInput struct {
	Ref                  string
	ValueType            string
	CanonicalValue       string
	Unit                 string
	Display              string
	NormalizationVersion int
	Metadata             map[string]any
}

type V2CommitPlacementSemanticResult struct {
	Status              string
	OutcomeID           string
	RelationshipResults []V2RelationshipDecisionResult
	SearchDocuments     []V2SearchDocumentResult
	EntityResolutionIDs []string
	ReviewTaskIDs       []string
}

var _ V2PlacementCommitRepository = (*V2LedgerRepositoryImpl)(nil)

func (r *V2LedgerRepositoryImpl) CommitPlacementSemanticResult(
	ctx context.Context,
	input V2CommitPlacementSemanticInput,
) (*V2CommitPlacementSemanticResult, error) {
	input = normalizeV2CommitPlacementSemanticInput(input)
	if err := validateV2CommitPlacementSemanticInput(input); err != nil {
		return nil, err
	}
	result := &V2CommitPlacementSemanticResult{}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockV2PlacementRunForCommit(ctx, tx, input); err != nil {
			return err
		}
		if err := lockV2PlacementItemForCommit(ctx, tx, input); err != nil {
			return err
		}
		if err := ensureV2PlacementItemCurrent(ctx, tx, input); err != nil {
			if errors.Is(err, ErrV2PlacementStaleSource) {
				outcomeID, outcomeErr := appendV2SupersededPlacementOutcome(ctx, tx, input)
				if outcomeErr != nil {
					return outcomeErr
				}
				if finishErr := finishV2PlacementRunIfTerminal(ctx, tx, input, string(domain.V2PlacementRunFailed)); finishErr != nil {
					return finishErr
				}
				result.Status = "superseded"
				result.OutcomeID = outcomeID
				return nil
			}
			return err
		}
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		placementFragmentID := ""
		if v2PlacementCommitNeedsPlacementFragmentID(input) {
			var err error
			placementFragmentID, err = loadV2PlacementItemFragmentID(ctx, tx, input)
			if err != nil {
				return err
			}
		}
		if v2PlacementEvidenceSearchableStatus(input.Status) {
			document, err := upsertV2PlacementItemEvidenceSearchDocument(ctx, tx, input, placementFragmentID)
			if err != nil {
				return err
			}
			appendV2PlacementSearchDocument(result, document)
		}
		entitiesByRef := make(map[string]string, len(input.EntityResolutions))
		for _, resolution := range input.EntityResolutions {
			resolutionID, entityID, err := insertV2PlacementEntityResolution(ctx, tx, input, resolution)
			if err != nil {
				return err
			}
			result.EntityResolutionIDs = append(result.EntityResolutionIDs, resolutionID)
			if entityID != "" {
				entitiesByRef[resolution.MentionRef] = entityID
			}
			if resolution.Action == string(domain.V2EntityResolutionAmbiguous) {
				taskID, err := insertV2EntityReviewTask(ctx, tx, input, resolution, resolutionID)
				if err != nil {
					return err
				}
				appendV2PlacementReviewTaskID(result, taskID)
			}
		}
		for _, observation := range input.RelationshipObservations {
			decision, err := v2RelationshipDecisionFromPlacementObservation(ctx, tx, input, observation, entitiesByRef)
			if err != nil {
				if !errors.Is(err, errV2PlacementUnresolvedEndpoint) {
					return err
				}
				review, reviewErr := insertV2RelationshipDependencyReview(ctx, tx, input, observation, err.Error())
				if reviewErr != nil {
					return reviewErr
				}
				appendV2PlacementRelationshipResult(result, review)
				appendV2PlacementReviewTaskID(result, review.ReviewTaskID)
				continue
			}
			if err := applyV2PlacementRelationshipDecision(ctx, tx, input, decision, observation.CorrectionTarget, placementFragmentID, result); err != nil {
				return err
			}
		}
		for _, review := range input.RelationshipReviews {
			recorded, err := insertV2RelationshipReview(ctx, tx, input, review)
			if err != nil {
				return err
			}
			appendV2PlacementRelationshipResult(result, recorded)
			appendV2PlacementReviewTaskID(result, recorded.ReviewTaskID)
		}
		for _, decision := range input.RelationshipDecisions {
			if err := applyV2PlacementRelationshipDecision(ctx, tx, input, withV2PlacementDecisionScope(input, decision), nil, placementFragmentID, result); err != nil {
				return err
			}
		}
		payload := v2PlacementCommitPayload(input.Payload, result)
		itemStatus := string(domain.V2PlacementRunCompleted)
		runStatus := string(domain.V2PlacementRunCompleted)
		if len(result.ReviewTaskIDs) > 0 {
			itemStatus = string(domain.V2PlacementRunAwaitingReview)
			runStatus = string(domain.V2PlacementRunAwaitingReview)
			input.Category = "candidate"
		}
		outcomeID, err := insertV2PlacementOutcome(ctx, tx, V2PlacementOutcomeInput{
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
		if err := updateV2PlacementItemOutcome(ctx, tx, V2PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementItemID:    input.PlacementItemID,
			UpdateItemStatus:   itemStatus,
			UpdateItemCategory: input.Category,
			Payload:            payload,
		}); err != nil {
			return err
		}
		if err := finishV2PlacementRunIfTerminal(ctx, tx, input, runStatus); err != nil {
			return err
		}
		result.Status = input.Status
		result.OutcomeID = outcomeID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 placement commit: %w", err)
	}
	return result, nil
}

func normalizeV2CommitPlacementSemanticInput(input V2CommitPlacementSemanticInput) V2CommitPlacementSemanticInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.MigrationRunID = strings.TrimSpace(input.MigrationRunID)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	input.Status = strings.TrimSpace(input.Status)
	input.Category = strings.TrimSpace(input.Category)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "semantic_commit"
	}
	if input.Status == "" {
		input.Status = string(domain.V2SemanticReviewAccepted)
	}
	if input.Category == "" {
		input.Category = "validated_claim"
	}
	for i := range input.EntityResolutions {
		resolution := &input.EntityResolutions[i]
		resolution.MentionRef = strings.TrimSpace(resolution.MentionRef)
		resolution.Action = strings.TrimSpace(resolution.Action)
		resolution.EntityID = strings.TrimSpace(resolution.EntityID)
		resolution.EntityKind = strings.TrimSpace(resolution.EntityKind)
		resolution.CanonicalName = strings.TrimSpace(resolution.CanonicalName)
		resolution.FragmentID = strings.TrimSpace(resolution.FragmentID)
		if resolution.EntityKind == "" {
			resolution.EntityKind = string(domain.V2EntityKindOther)
		}
	}
	for i := range input.RelationshipObservations {
		observation := &input.RelationshipObservations[i]
		*observation = normalizeV2PlacementRelationshipDecisionInput(*observation)
	}
	for i := range input.RelationshipReviews {
		review := &input.RelationshipReviews[i]
		review.Ref = strings.TrimSpace(review.Ref)
		review.SubjectRef = strings.TrimSpace(review.SubjectRef)
		review.OriginalPredicate = strings.TrimSpace(review.OriginalPredicate)
		review.ObjectRef = strings.TrimSpace(review.ObjectRef)
		review.Polarity = strings.TrimSpace(review.Polarity)
		review.EvidenceVerdict = strings.TrimSpace(review.EvidenceVerdict)
		review.Reason = strings.TrimSpace(review.Reason)
		if review.Polarity == "" {
			review.Polarity = "+"
		}
		if review.Reason == "" {
			review.Reason = "relationship_needs_review"
		}
		if review.ObjectValue != nil {
			value := normalizeV2PlacementValueInput(*review.ObjectValue)
			review.ObjectValue = &value
		}
	}
	return input
}

func validateV2CommitPlacementSemanticInput(input V2CommitPlacementSemanticInput) error {
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
	if err := validateV2PlacementMigrationFence(input.MigrationRunID, input.MigrationEpoch); err != nil {
		return err
	}
	if input.Status == "" {
		return errors.New("status is required")
	}
	if input.Category != "candidate" && input.Category != "validated_claim" && input.Category != "fact" {
		return fmt.Errorf("unsupported placement category %q", input.Category)
	}
	for _, resolution := range input.EntityResolutions {
		if err := validateV2PlacementEntityResolutionInput(resolution); err != nil {
			return err
		}
	}
	for _, observation := range input.RelationshipObservations {
		if err := validateV2PlacementRelationshipDecisionInput(observation); err != nil {
			return err
		}
	}
	for _, review := range input.RelationshipReviews {
		if err := validateV2PlacementRelationshipReviewInput(review); err != nil {
			return err
		}
	}
	for _, decision := range input.RelationshipDecisions {
		scoped := withV2PlacementDecisionScope(input, decision)
		if err := validateV2ApplyRelationshipDecisionInput(normalizeV2ApplyRelationshipDecisionInput(scoped)); err != nil {
			return err
		}
	}
	return nil
}

func v2PlacementCommitNeedsPlacementFragmentID(input V2CommitPlacementSemanticInput) bool {
	if v2PlacementEvidenceSearchableStatus(input.Status) {
		return true
	}
	for _, observation := range input.RelationshipObservations {
		if observation.Support != nil && strings.TrimSpace(observation.Support.FragmentID) != "" {
			return true
		}
	}
	for _, decision := range input.RelationshipDecisions {
		if decision.Support != nil && strings.TrimSpace(decision.Support.FragmentID) != "" {
			return true
		}
	}
	return false
}

func validateV2PlacementEntityResolutionInput(input V2PlacementEntityResolutionInput) error {
	if input.MentionRef == "" {
		return errors.New("entity resolution mention_ref is required")
	}
	switch input.Action {
	case string(domain.V2EntityResolutionReuse):
		if _, err := uuid.Parse(input.EntityID); err != nil {
			return fmt.Errorf("entity resolution entity_id is required: %w", err)
		}
	case string(domain.V2EntityResolutionCreate):
		if input.CanonicalName == "" {
			return errors.New("entity resolution canonical_name is required")
		}
		if !v2Contains(domain.V2EntityKinds(), input.EntityKind) {
			return fmt.Errorf("unsupported entity_kind %q", input.EntityKind)
		}
	case string(domain.V2EntityResolutionAmbiguous):
		return nil
	default:
		return fmt.Errorf("unsupported entity resolution action %q", input.Action)
	}
	return nil
}

func normalizeV2PlacementRelationshipDecisionInput(input V2PlacementRelationshipDecisionInput) V2PlacementRelationshipDecisionInput {
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
		input.EvidenceVerdict = string(domain.V2VerificationEntailed)
	}
	if input.ObjectValue != nil {
		value := normalizeV2PlacementValueInput(*input.ObjectValue)
		input.ObjectValue = &value
	}
	if input.Support != nil {
		input.Support.FragmentID = strings.TrimSpace(input.Support.FragmentID)
		input.Support.SourceGroupKey = strings.TrimSpace(input.Support.SourceGroupKey)
		input.Support.SourceID = strings.TrimSpace(input.Support.SourceID)
		input.Support.SourceRevisionID = strings.TrimSpace(input.Support.SourceRevisionID)
		input.Support.Quote = strings.TrimSpace(input.Support.Quote)
		input.Support.Authority = strings.TrimSpace(input.Support.Authority)
		if input.Support.Authority == "" {
			input.Support.Authority = "primary"
		}
	}
	if input.CorrectionTarget != nil {
		target := *input.CorrectionTarget
		target.RelationshipID = strings.TrimSpace(target.RelationshipID)
		input.CorrectionTarget = &target
	}
	return input
}

func validateV2PlacementRelationshipDecisionInput(input V2PlacementRelationshipDecisionInput) error {
	if input.SubjectRef == "" {
		return errors.New("relationship observation subject_ref is required")
	}
	if input.PredicateKey == "" {
		return errors.New("relationship observation predicate_key is required")
	}
	if input.PredicateVersion < 1 {
		return errors.New("relationship observation predicate_version must be greater than zero")
	}
	if (input.ObjectRef == "") == (input.ObjectValue == nil) {
		return errors.New("relationship observation requires exactly one object endpoint")
	}
	if input.ObjectValue != nil {
		if err := validateV2PlacementValueInput(*input.ObjectValue); err != nil {
			return err
		}
	}
	if input.Polarity != "+" && input.Polarity != "-" {
		return fmt.Errorf("unsupported relationship observation polarity %q", input.Polarity)
	}
	if !v2Contains(domain.V2VerificationVerdicts(), input.EvidenceVerdict) {
		return fmt.Errorf("unsupported relationship observation evidence_verdict %q", input.EvidenceVerdict)
	}
	if input.Confidence != nil && (*input.Confidence < 0 || *input.Confidence > 1) {
		return errors.New("relationship observation confidence must be between 0 and 1")
	}
	if input.ValidFrom != nil && input.ValidTo != nil && input.ValidTo.Before(*input.ValidFrom) {
		return errors.New("relationship observation valid_to must be greater than or equal to valid_from")
	}
	if input.EvidenceVerdict == string(domain.V2VerificationEntailed) {
		if input.Support == nil {
			return errors.New("entailed relationship observations require support")
		}
		if err := validateV2EvidenceSupportInput(*input.Support); err != nil {
			return err
		}
	}
	if input.CorrectionTarget != nil {
		if err := validateV2PlacementCorrectionTargetInput(*input.CorrectionTarget); err != nil {
			return err
		}
	}
	return nil
}

func validateV2PlacementCorrectionTargetInput(input V2PlacementCorrectionTargetInput) error {
	if _, err := uuid.Parse(input.RelationshipID); err != nil {
		return fmt.Errorf("correction target relationship_id is required: %w", err)
	}
	if input.ExpectedVersion < 1 {
		return errors.New("correction target expected_version must be greater than zero")
	}
	return nil
}

func normalizeV2PlacementValueInput(input V2PlacementValueInput) V2PlacementValueInput {
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

func validateV2PlacementValueInput(input V2PlacementValueInput) error {
	if !v2Contains(domain.V2ValueTypes(), input.ValueType) {
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

func lockV2PlacementRunForCommit(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) error {
	if err := lockV2MigrationRunForPlacementCommit(ctx, tx, input); err != nil {
		return err
	}
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
		  AND (? = '' OR COALESCE(migration_claim_epoch, 0) = ?)
		  AND lease_until > clock_timestamp()
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID,
		input.WorkerID, input.ExpectedAttempts, input.MigrationRunID, input.MigrationEpoch).Scan(&found).Error
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrV2PlacementLeaseLost
	}
	return nil
}

func lockV2PlacementItemForCommit(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) error {
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
		return ErrV2PlacementLeaseLost
	}
	return nil
}

func ensureV2PlacementItemCurrent(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) error {
	var sourceRevisionID, currentRevisionID sql.NullString
	err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(source.current_revision_id::text, '')
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
	`, input.TeamID, input.OwnerProfileID, input.PlacementItemID).Row().Scan(&sourceRevisionID, &currentRevisionID)
	if err != nil {
		return err
	}
	if sourceRevisionID.String != "" && currentRevisionID.String != "" && sourceRevisionID.String != currentRevisionID.String {
		return ErrV2PlacementStaleSource
	}
	return nil
}

func appendV2SupersededPlacementOutcome(ctx context.Context, tx *gorm.DB, input V2CommitPlacementSemanticInput) (string, error) {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"status":           "superseded",
		"reason":           "source revision changed before semantic commit",
	}
	outcomeID, err := insertV2PlacementOutcome(ctx, tx, V2PlacementOutcomeInput{
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
	return outcomeID, updateV2PlacementItemOutcome(ctx, tx, V2PlacementOutcomeInput{
		TeamID:             input.TeamID,
		OwnerProfileID:     input.OwnerProfileID,
		PlacementItemID:    input.PlacementItemID,
		UpdateItemStatus:   "failed",
		UpdateItemCategory: "failed",
		Payload:            payload,
	})
}

func insertV2PlacementEntityResolution(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementEntityResolutionInput,
) (string, string, error) {
	entityID := input.EntityID
	if input.Action == string(domain.V2EntityResolutionCreate) {
		created, err := insertV2PlacementEntity(ctx, tx, commit, input)
		if err != nil {
			return "", "", err
		}
		entityID = created
	}
	verifierResult, err := marshalV2JSON(input.VerifierResult)
	if err != nil {
		return "", "", err
	}
	metadata, err := marshalV2JSON(input.Metadata)
	if err != nil {
		return "", "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_resolution_events (
		    team_id, ingest_id, placement_item_id, owner_profile_id, mention_ref,
		    action, entity_id, fragment_id, span_start, span_end, verifier_result, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid,
		    NULLIF(?, '')::uuid, ?, ?, ?::jsonb, ?::jsonb
		)
		RETURNING resolution_event_id::text
	`, commit.TeamID, commit.IngestID, commit.PlacementItemID, commit.OwnerProfileID,
		input.MentionRef, input.Action, entityID, input.FragmentID,
		v2IntPointerArg(input.SpanStart), v2IntPointerArg(input.SpanEnd),
		string(verifierResult), string(metadata)).Rows()
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

func insertV2EntityReviewTask(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementEntityResolutionInput,
	resolutionID string,
) (string, error) {
	payload, err := marshalV2JSON(map[string]any{
		"mention_ref":         input.MentionRef,
		"resolution_event_id": resolutionID,
		"action":              input.Action,
		"entity_kind":         input.EntityKind,
		"canonical_name":      input.CanonicalName,
		"reason":              "ambiguous_entity",
	})
	if err != nil {
		return "", err
	}
	dedupeKey := "identity:" + commit.PlacementItemID + ":" + input.MentionRef
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO review_tasks (
		    team_id, owner_profile_id, ingest_id, placement_item_id,
		    task_type, status, reason, payload, dedupe_key, updated_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    'identity_needs_review', 'open', 'ambiguous_entity', ?::jsonb, ?, now()
		)
		ON CONFLICT (team_id, dedupe_key)
		WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged')
		DO UPDATE SET updated_at = now()
		RETURNING review_task_id::text
	`, commit.TeamID, commit.OwnerProfileID, commit.IngestID, commit.PlacementItemID,
		string(payload), dedupeKey).Rows()
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

func insertV2PlacementEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementEntityResolutionInput,
) (string, error) {
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

func v2RelationshipDecisionFromPlacementObservation(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementRelationshipDecisionInput,
	entitiesByRef map[string]string,
) (V2ApplyRelationshipDecisionInput, error) {
	subjectID := entitiesByRef[input.SubjectRef]
	if subjectID == "" {
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf("%w: relationship observation %q references subject_ref %q", errV2PlacementUnresolvedEndpoint, input.Ref, input.SubjectRef)
	}
	decision := V2ApplyRelationshipDecisionInput{
		TeamID:               commit.TeamID,
		OwnerProfileID:       commit.OwnerProfileID,
		IngestID:             commit.IngestID,
		PlacementItemID:      commit.PlacementItemID,
		ProposalRef:          input.Ref,
		SubjectRef:           input.SubjectRef,
		SubjectEntityID:      subjectID,
		OriginalPredicate:    input.OriginalPredicate,
		PredicateKey:         input.PredicateKey,
		PredicateVersion:     input.PredicateVersion,
		Polarity:             input.Polarity,
		ScopeKey:             input.ScopeKey,
		ValidFrom:            input.ValidFrom,
		ValidTo:              input.ValidTo,
		EvidenceVerdict:      input.EvidenceVerdict,
		PromoteToFact:        input.PromoteToFact,
		Confidence:           input.Confidence,
		Rationale:            input.Rationale,
		Model:                input.Model,
		ResponseHash:         input.ResponseHash,
		Support:              input.Support,
		ObservationMetadata:  input.ObservationMetadata,
		RelationshipMetadata: input.RelationshipMetadata,
	}
	if input.ObjectValue != nil {
		value, err := upsertV2PlacementValue(ctx, tx, commit, *input.ObjectValue)
		if err != nil {
			return V2ApplyRelationshipDecisionInput{}, err
		}
		decision.ObjectRef = input.ObjectValue.Ref
		if decision.ObjectRef == "" {
			decision.ObjectRef = value.ValueID
		}
		decision.ObjectValueID = value.ValueID
	} else {
		objectID := entitiesByRef[input.ObjectRef]
		if objectID == "" {
			return V2ApplyRelationshipDecisionInput{}, fmt.Errorf("%w: relationship observation %q references object_ref %q", errV2PlacementUnresolvedEndpoint, input.Ref, input.ObjectRef)
		}
		decision.ObjectRef = input.ObjectRef
		decision.ObjectEntityID = objectID
	}
	decision = normalizeV2ApplyRelationshipDecisionInput(decision)
	if err := validateV2ApplyRelationshipDecisionInput(decision); err != nil {
		return V2ApplyRelationshipDecisionInput{}, err
	}
	return decision, nil
}

func upsertV2PlacementValue(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementValueInput,
) (*V2ValueRecord, error) {
	metadata, err := marshalV2JSON(input.Metadata)
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
	loaded := V2ValueRecord{}
	if err := rows.Scan(&loaded.TeamID, &loaded.ValueID, &loaded.ValueType, &loaded.CanonicalValue,
		&loaded.Unit, &loaded.Display, &loaded.NormalizationVersion, &loaded.Existing); err != nil {
		return nil, err
	}
	return &loaded, rows.Err()
}

func applyV2PlacementRelationshipDecision(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	decision V2ApplyRelationshipDecisionInput,
	correctionTarget *V2PlacementCorrectionTargetInput,
	placementFragmentID string,
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
			document, err := upsertV2PlacementEvidenceSearchDocument(ctx, tx, commit, decision.Support.FragmentID, map[string]any{
				"supporting_placement_item_id": commit.PlacementItemID,
				"support_id":                   applied.SupportID,
				"relationship_id":              applied.Relationship.RelationshipID,
			})
			if err != nil {
				return err
			}
			appendV2PlacementSearchDocument(result, document)
		}
	}
	document, err := upsertV2PlacementRelationshipSearchDocument(ctx, tx, commit, applied.Relationship)
	if err != nil {
		return err
	}
	appendV2PlacementSearchDocument(result, document)
	return nil
}
