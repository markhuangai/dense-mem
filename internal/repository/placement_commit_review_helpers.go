package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func insertRelationshipDependencyReview(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementRelationshipDecisionInput,
	reason string,
) (*RelationshipDecisionResult, error) {
	objectRef := input.ObjectRef
	if objectRef == "" && input.ObjectValue != nil {
		objectRef = input.ObjectValue.Ref
	}
	return insertRelationshipReview(ctx, tx, commit, PlacementRelationshipReviewInput{
		Ref:               input.Ref,
		SubjectRef:        input.SubjectRef,
		OriginalPredicate: input.OriginalPredicate,
		ObjectRef:         objectRef,
		ObjectValue:       input.ObjectValue,
		Polarity:          input.Polarity,
		EvidenceVerdict:   input.EvidenceVerdict,
		Confidence:        input.Confidence,
		Rationale:         input.Rationale,
		Model:             input.Model,
		ResponseHash:      input.ResponseHash,
		Support:           input.Support,
		Reason:            "identity_needs_review",
		Payload: map[string]any{
			"error": reason,
		},
	})
}

func insertRelationshipPredicateReview(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementRelationshipDecisionInput,
	reason string,
) (*RelationshipDecisionResult, error) {
	objectRef := input.ObjectRef
	if objectRef == "" && input.ObjectValue != nil {
		objectRef = input.ObjectValue.Ref
	}
	return insertRelationshipReview(ctx, tx, commit, PlacementRelationshipReviewInput{
		Ref:               input.Ref,
		SubjectRef:        input.SubjectRef,
		OriginalPredicate: input.OriginalPredicate,
		ObjectRef:         objectRef,
		ObjectValue:       input.ObjectValue,
		Polarity:          input.Polarity,
		EvidenceVerdict:   input.EvidenceVerdict,
		Confidence:        input.Confidence,
		Rationale:         input.Rationale,
		Model:             input.Model,
		ResponseHash:      input.ResponseHash,
		Support:           input.Support,
		Reason:            "predicate_needs_review",
		Payload: map[string]any{
			"error":                    reason,
			"selected_predicate_key":   input.PredicateKey,
			"predicate_policy_version": domain.PredicatePolicyVersion,
		},
	})
}

func validatePlacementRelationshipReviewInput(input PlacementRelationshipReviewInput) error {
	if input.Ref == "" {
		return errors.New("relationship review ref is required")
	}
	if input.SubjectRef == "" {
		return errors.New("relationship review subject_ref is required")
	}
	if input.OriginalPredicate == "" {
		return errors.New("relationship review original_predicate is required")
	}
	if (input.ObjectRef == "") == (input.ObjectValue == nil) {
		return errors.New("relationship review requires exactly one object endpoint")
	}
	if input.ObjectValue != nil {
		if err := validatePlacementValueInput(*input.ObjectValue); err != nil {
			return err
		}
	}
	if input.Polarity != "+" && input.Polarity != "-" {
		return fmt.Errorf("unsupported relationship review polarity %q", input.Polarity)
	}
	if input.EvidenceVerdict != "" && !contains(domain.VerificationVerdicts(), input.EvidenceVerdict) {
		return fmt.Errorf("unsupported relationship review evidence_verdict %q", input.EvidenceVerdict)
	}
	if input.Confidence != nil && (*input.Confidence < 0 || *input.Confidence > 1) {
		return errors.New("relationship review confidence must be between 0 and 1")
	}
	if input.Support != nil {
		if err := validateEvidenceSupportInput(*input.Support); err != nil {
			return err
		}
	}
	return nil
}

func insertRelationshipReview(
	ctx context.Context,
	tx *gorm.DB,
	commit CommitPlacementSemanticInput,
	input PlacementRelationshipReviewInput,
) (*RelationshipDecisionResult, error) {
	objectRef := input.ObjectRef
	objectValueID := ""
	if input.ObjectValue != nil {
		value, err := upsertPlacementValue(ctx, tx, commit, *input.ObjectValue)
		if err != nil {
			return nil, err
		}
		objectRef = input.ObjectValue.Ref
		if objectRef == "" {
			objectRef = value.ValueID
		}
		objectValueID = value.ValueID
	}
	observationMetadata := map[string]any{
		"review_reason": input.Reason,
	}
	if input.ObjectValue != nil {
		observationMetadata["object_value_ref"] = input.ObjectValue.Ref
		observationMetadata["object_value_type"] = input.ObjectValue.ValueType
	}
	observationID, err := insertRelationshipObservation(ctx, tx, ApplyRelationshipDecisionInput{
		TeamID:              commit.TeamID,
		OwnerProfileID:      commit.OwnerProfileID,
		IngestID:            commit.IngestID,
		PlacementItemID:     commit.PlacementItemID,
		ProposalRef:         input.Ref,
		SubjectRef:          input.SubjectRef,
		OriginalPredicate:   input.OriginalPredicate,
		ObjectRef:           objectRef,
		ObjectValueID:       objectValueID,
		Polarity:            input.Polarity,
		EvidenceVerdict:     input.EvidenceVerdict,
		Support:             input.Support,
		ObservationMetadata: observationMetadata,
	}, "")
	if err != nil {
		return nil, err
	}
	verificationID := ""
	if input.EvidenceVerdict != "" {
		verificationID, err = insertVerificationEvent(ctx, tx, ApplyRelationshipDecisionInput{
			TeamID:          commit.TeamID,
			OwnerProfileID:  commit.OwnerProfileID,
			EvidenceVerdict: input.EvidenceVerdict,
			Confidence:      input.Confidence,
			Rationale:       input.Rationale,
			Model:           input.Model,
			ResponseHash:    input.ResponseHash,
		}, observationID)
		if err != nil {
			return nil, err
		}
	}
	taskType := "relationship_needs_review"
	reason := input.Reason
	if strings.Contains(reason, "predicate") {
		taskType = "predicate_needs_review"
		reason = "predicate_needs_review"
	} else if strings.Contains(reason, "identity") {
		taskType = "identity_needs_review"
		reason = "identity_needs_review"
	}
	payload := map[string]any{
		"relationship_ref":   input.Ref,
		"subject_ref":        input.SubjectRef,
		"object_ref":         objectRef,
		"original_predicate": input.OriginalPredicate,
		"polarity":           input.Polarity,
		"evidence_verdict":   input.EvidenceVerdict,
		"reason":             reason,
	}
	if input.ObjectValue != nil {
		payload["object_value"] = map[string]any{
			"ref":             input.ObjectValue.Ref,
			"value_type":      input.ObjectValue.ValueType,
			"canonical_value": input.ObjectValue.CanonicalValue,
			"display":         input.ObjectValue.Display,
			"unit":            input.ObjectValue.Unit,
		}
	}
	for key, value := range input.Payload {
		payload[key] = value
	}
	if taskType == "predicate_needs_review" {
		payload["predicate_policy_version"] = domain.PredicatePolicyVersion
	}
	payloadJSON, err := marshalJSON(payload)
	if err != nil {
		return nil, err
	}
	dedupeKey := "relationship:" + commit.PlacementItemID + ":" + input.Ref + ":" + taskType
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO review_tasks (
		    team_id, owner_profile_id, ingest_id, placement_item_id,
		    observation_id, task_type, status, reason, payload, dedupe_key, updated_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    ?::uuid, ?, 'open', ?, ?::jsonb, ?, now()
		)
		ON CONFLICT (team_id, dedupe_key)
		WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged')
		DO UPDATE SET updated_at = now()
		RETURNING review_task_id::text
	`, commit.TeamID, commit.OwnerProfileID, commit.IngestID, commit.PlacementItemID,
		observationID, taskType, reason, string(payloadJSON), dedupeKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var taskID string
	if err := rows.Scan(&taskID); err != nil {
		return nil, err
	}
	return &RelationshipDecisionResult{
		ObservationID:       observationID,
		VerificationEventID: verificationID,
		ReviewTaskID:        taskID,
		ProposalID:          input.Ref,
		OwnerProfileID:      commit.OwnerProfileID,
		Category:            taskType,
		Reason:              reason,
	}, rows.Err()
}

func insertRelationshipValidToReview(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
	canonical *RelationshipRecord,
) (*RelationshipDecisionResult, error) {
	if canonical == nil {
		return nil, errors.New("canonical relationship is required for valid_to review")
	}
	observationMetadata := make(map[string]any, len(input.ObservationMetadata)+3)
	for key, value := range input.ObservationMetadata {
		observationMetadata[key] = value
	}
	observationMetadata["review_reason"] = "relationship_identity_valid_to_conflict"
	observationMetadata["canonical_relationship_id"] = canonical.RelationshipID
	observationMetadata["canonical_valid_to"] = reviewTimeValue(canonical.ValidTo)
	input.ObservationMetadata = observationMetadata

	observationID, err := insertRelationshipObservation(ctx, tx, input, "")
	if err != nil {
		return nil, err
	}
	verificationID, err := insertVerificationEvent(ctx, tx, input, observationID)
	if err != nil {
		return nil, err
	}
	payload, err := marshalJSON(map[string]any{
		"relationship_ref":          input.ProposalRef,
		"canonical_relationship_id": canonical.RelationshipID,
		"subject_entity_id":         input.SubjectEntityID,
		"predicate_key":             input.PredicateKey,
		"predicate_version":         input.PredicateVersion,
		"object_entity_id":          input.ObjectEntityID,
		"object_value_id":           input.ObjectValueID,
		"polarity":                  input.Polarity,
		"scope_key":                 input.ScopeKey,
		"valid_from":                reviewTimeValue(input.ValidFrom),
		"canonical_valid_to":        reviewTimeValue(canonical.ValidTo),
		"proposed_valid_to":         reviewTimeValue(input.ValidTo),
		"evidence_verdict":          input.EvidenceVerdict,
		"reason":                    "relationship_identity_valid_to_conflict",
	})
	if err != nil {
		return nil, err
	}
	proposalKey := input.ProposalRef
	if proposalKey == "" {
		proposalKey = observationID
	}
	dedupeKey := strings.Join([]string{
		"relationship_valid_to",
		canonical.RelationshipID,
		input.IngestID,
		proposalKey,
	}, ":")
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO review_tasks (
		    team_id, owner_profile_id, ingest_id, placement_item_id,
		    relationship_id, observation_id, task_type, status,
		    reason, payload, dedupe_key, updated_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid,
		    ?::uuid, ?::uuid, 'relationship_needs_review', 'open',
		    'relationship_identity_valid_to_conflict', ?::jsonb, ?, now()
		)
		ON CONFLICT (team_id, dedupe_key)
		WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged')
		DO UPDATE SET updated_at = now()
		RETURNING review_task_id::text
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementItemID,
		canonical.RelationshipID, observationID, string(payload), dedupeKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var reviewTaskID string
	if err := rows.Scan(&reviewTaskID); err != nil {
		return nil, err
	}
	return &RelationshipDecisionResult{
		ObservationID:       observationID,
		VerificationEventID: verificationID,
		ReviewTaskID:        reviewTaskID,
		ProposalID:          input.ProposalRef,
		OwnerProfileID:      input.OwnerProfileID,
		Category:            string(domain.OutcomeRelationshipNeedsReview),
		Reason:              "relationship_identity_valid_to_conflict",
	}, rows.Err()
}

func reviewTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
