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
	ErrV2PlacementLeaseLost   = errors.New("v2 placement lease lost")
	ErrV2PlacementStaleSource = errors.New("v2 placement stale source")
)

type V2PlacementCommitRepository interface {
	CommitPlacementSemanticResult(ctx context.Context, input V2CommitPlacementSemanticInput) (*V2CommitPlacementSemanticResult, error)
	CompletePlacementReviewResult(ctx context.Context, input V2CompletePlacementReviewInput) (*V2CompletePlacementReviewResult, error)
}

type V2CommitPlacementSemanticInput struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	PlacementItemID  string
	WorkerID         string
	ExpectedAttempts int
	OutcomeKind      string
	Status           string
	Category         string
	Payload          map[string]any

	EntityResolutions        []V2PlacementEntityResolutionInput
	RelationshipObservations []V2PlacementRelationshipDecisionInput
	RelationshipDecisions    []V2ApplyRelationshipDecisionInput
}

type V2PlacementEntityResolutionInput struct {
	MentionRef     string
	Action         string
	EntityID       string
	EntityKind     string
	CanonicalName  string
	FragmentID     string
	SpanStart      *int
	SpanEnd        *int
	VerifierResult map[string]any
	Metadata       map[string]any
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
	ObservationMetadata  map[string]any
	RelationshipMetadata map[string]any
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
		}
		for _, observation := range input.RelationshipObservations {
			decision, err := v2RelationshipDecisionFromPlacementObservation(ctx, tx, input, observation, entitiesByRef)
			if err != nil {
				return err
			}
			if err := applyV2PlacementRelationshipDecision(ctx, tx, input, decision, result); err != nil {
				return err
			}
		}
		for _, decision := range input.RelationshipDecisions {
			if err := applyV2PlacementRelationshipDecision(ctx, tx, input, withV2PlacementDecisionScope(input, decision), result); err != nil {
				return err
			}
		}
		payload := v2PlacementCommitPayload(input.Payload, result)
		outcomeID, err := insertV2PlacementOutcome(ctx, tx, V2PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementRunID:     input.PlacementRunID,
			PlacementItemID:    input.PlacementItemID,
			OutcomeKind:        input.OutcomeKind,
			Status:             input.Status,
			Payload:            payload,
			UpdateItemStatus:   string(domain.V2PlacementRunCompleted),
			UpdateItemCategory: input.Category,
		})
		if err != nil {
			return err
		}
		if err := updateV2PlacementItemOutcome(ctx, tx, V2PlacementOutcomeInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			PlacementItemID:    input.PlacementItemID,
			UpdateItemStatus:   string(domain.V2PlacementRunCompleted),
			UpdateItemCategory: input.Category,
			Payload:            payload,
		}); err != nil {
			return err
		}
		if err := finishV2PlacementRunIfTerminal(ctx, tx, input, string(domain.V2PlacementRunCompleted)); err != nil {
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
	for _, decision := range input.RelationshipDecisions {
		scoped := withV2PlacementDecisionScope(input, decision)
		if err := validateV2ApplyRelationshipDecisionInput(normalizeV2ApplyRelationshipDecisionInput(scoped)); err != nil {
			return err
		}
	}
	return nil
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

func insertV2PlacementEntity(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementEntityResolutionInput,
) (string, error) {
	identityContext, err := marshalV2JSON(map[string]any{
		"source":      "semantic_placement",
		"mention_ref": input.MentionRef,
	})
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
		return V2ApplyRelationshipDecisionInput{}, fmt.Errorf("relationship observation %q references unresolved subject_ref %q", input.Ref, input.SubjectRef)
	}
	decision := V2ApplyRelationshipDecisionInput{
		TeamID:               commit.TeamID,
		OwnerProfileID:       commit.OwnerProfileID,
		IngestID:             commit.IngestID,
		PlacementItemID:      commit.PlacementItemID,
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
			return V2ApplyRelationshipDecisionInput{}, fmt.Errorf("relationship observation %q references unresolved object_ref %q", input.Ref, input.ObjectRef)
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
	result *V2CommitPlacementSemanticResult,
) error {
	applied, err := applyV2RelationshipDecisionInTx(ctx, tx, decision)
	if err != nil {
		return err
	}
	result.RelationshipResults = append(result.RelationshipResults, *applied)
	if applied.Relationship == nil || applied.Relationship.Status != string(domain.V2RelationshipStatusActive) {
		return nil
	}
	document, err := upsertV2PlacementRelationshipSearchDocument(ctx, tx, commit, applied.Relationship)
	if err != nil {
		return err
	}
	result.SearchDocuments = append(result.SearchDocuments, *document)
	return nil
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
		if err := insertV2RelationshipTransition(ctx, tx, v2TransitionInput{
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
	payload["search_document_ids"] = searchDocuments
	payload["embedding_job_ids"] = embeddingJobs
	payload["entity_resolution_ids"] = append([]string(nil), result.EntityResolutionIDs...)
	return payload
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

func v2IntPointerArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
