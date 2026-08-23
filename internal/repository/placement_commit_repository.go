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
	ErrPlacementLeaseLost                = errors.New("placement lease lost")
	ErrPlacementStaleSource              = errors.New("placement stale source")
	ErrConflictContextStale              = errors.New("conflict context stale")
	ErrRememberExactReferenceStale       = errors.New("remember exact reference stale")
	ErrCorrectionTargetStale             = errors.New("correction target stale")
	errPlacementUnresolvedEndpoint       = errors.New("placement unresolved relationship endpoint")
	errPlacementPredicateUnresolved      = errors.New("placement predicate cannot be resolved safely")
	errRelationshipDecisionNonPromotable = errors.New("relationship decision is not promotable")
)

func validatePlacementEntityResolutionInput(input PlacementEntityResolutionInput) error {
	if input.AssessmentID != "" {
		if _, err := uuid.Parse(input.AssessmentID); err != nil {
			return fmt.Errorf("entity resolution assessment_id is invalid: %w", err)
		}
	}
	if input.MentionRef == "" {
		return errors.New("entity resolution mention_ref is required")
	}
	if input.ExactEntityID != "" {
		if _, err := uuid.Parse(input.ExactEntityID); err != nil {
			return fmt.Errorf("entity resolution exact_entity_id is invalid: %w", err)
		}
		if input.Action != string(domain.EntityResolutionReuse) || input.EntityID != input.ExactEntityID {
			return ErrRememberExactReferenceStale
		}
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
	input.ExactPredicateKey = strings.TrimSpace(input.ExactPredicateKey)
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
	if input.EvidenceVerdict == "" && !input.AssessorAccepted {
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
	if input.AssessorAccepted {
		if _, err := uuid.Parse(input.AssessmentID); err != nil {
			return fmt.Errorf("assessor accepted relationship assessment_id is required: %w", err)
		}
		if input.PredicateCandidate != nil || input.SuppressSupport {
			return errRelationshipDecisionNonPromotable
		}
	} else {
		if err := validateAssessmentDecisionAudit(input.AssessmentID, input.AssessmentPolicyVersion, input.ThresholdUsed, input.GateResult, input.SuppressSupport); err != nil {
			return err
		}
	}
	if input.SubjectRef == "" {
		return errors.New("relationship observation subject_ref is required")
	}
	if input.ExactPredicateKey != "" && input.PredicateKey != input.ExactPredicateKey {
		return ErrRememberExactReferenceStale
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
	if !input.AssessorAccepted {
		if !contains(domain.VerificationVerdicts(), input.EvidenceVerdict) {
			return fmt.Errorf("unsupported relationship observation evidence_verdict %q", input.EvidenceVerdict)
		}
		if input.Confidence != nil && (*input.Confidence < 0 || *input.Confidence > 1) {
			return errors.New("relationship observation confidence must be between 0 and 1")
		}
	}
	if input.ValidFrom != nil && input.ValidTo != nil && input.ValidTo.Before(*input.ValidFrom) {
		return errors.New("relationship observation valid_to must be greater than or equal to valid_from")
	}
	if (input.AssessorAccepted || input.EvidenceVerdict == string(domain.VerificationEntailed)) && !input.SuppressSupport {
		if len(relationshipEvidenceSupports(input.Support, input.Supports)) == 0 {
			return errors.New("accepted relationship observations require support")
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

func insertPlacementEntityResolution(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementEntityResolutionInput,
) (string, string, error) {
	if input.ExactEntityID != "" {
		var status string
		err := withSystemModeInTx(ctx, tx, commit.TeamID, commit.OwnerProfileID, func(systemTx *gorm.DB) error {
			return systemTx.WithContext(ctx).Raw(`
				SELECT status FROM entity_records
				WHERE team_id = ?::uuid AND entity_id = ?::uuid
				LIMIT 1
				FOR SHARE
			`, commit.TeamID, input.ExactEntityID).Row().Scan(&status)
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", ErrRememberExactReferenceStale
			}
			return "", "", err
		}
		if status != "active" {
			return "", "", ErrRememberExactReferenceStale
		}
	}
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
		    assessment_id, space_id, space_generation
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid,
		    NULLIF(?, '')::uuid, ?, ?, ?::jsonb, ?::jsonb, NULLIF(?, '')::uuid,
		    (SELECT item.space_id FROM placement_items AS item
		     WHERE item.team_id = ?::uuid AND item.placement_item_id = ?::uuid
		       AND item.ingest_id = ?::uuid AND item.owner_profile_id = ?::uuid),
		    (SELECT item.space_generation FROM placement_items AS item
		     WHERE item.team_id = ?::uuid AND item.placement_item_id = ?::uuid
		       AND item.ingest_id = ?::uuid AND item.owner_profile_id = ?::uuid)
		)
		RETURNING resolution_event_id::text
	`, commit.TeamID, commit.IngestID, commit.PlacementItemID, commit.OwnerProfileID,
		input.MentionRef, input.Action, entityID, input.FragmentID,
		intPointerArg(input.SpanStart), intPointerArg(input.SpanEnd),
		string(verifierResult), string(metadata), input.AssessmentID,
		commit.TeamID, commit.PlacementItemID, commit.IngestID, commit.OwnerProfileID,
		commit.TeamID, commit.PlacementItemID, commit.IngestID, commit.OwnerProfileID).Rows()
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
		AssessorAccepted:        input.AssessorAccepted,
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
			    normalization_version, metadata, space_id, space_generation
			) VALUES (
			    ?::uuid, ?, ?, NULLIF(?, ''), ?, ?, ?::jsonb,
			    (SELECT ingest.space_id FROM knowledge_ingests AS ingest
			     WHERE ingest.team_id = ?::uuid AND ingest.ingest_id = ?::uuid
			       AND ingest.owner_profile_id = ?::uuid),
			    (SELECT ingest.space_generation FROM knowledge_ingests AS ingest
			     WHERE ingest.team_id = ?::uuid AND ingest.ingest_id = ?::uuid
			       AND ingest.owner_profile_id = ?::uuid)
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
		commit.TeamID, commit.IngestID, commit.OwnerProfileID,
		commit.TeamID, commit.IngestID, commit.OwnerProfileID,
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
