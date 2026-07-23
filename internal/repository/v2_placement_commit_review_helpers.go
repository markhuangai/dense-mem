package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

func insertV2RelationshipDependencyReview(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementRelationshipDecisionInput,
	reason string,
) (*V2RelationshipDecisionResult, error) {
	objectRef := input.ObjectRef
	if objectRef == "" && input.ObjectValue != nil {
		objectRef = input.ObjectValue.Ref
	}
	return insertV2RelationshipReview(ctx, tx, commit, V2PlacementRelationshipReviewInput{
		Ref:               input.Ref,
		SubjectRef:        input.SubjectRef,
		OriginalPredicate: input.OriginalPredicate,
		ObjectRef:         objectRef,
		ObjectValue:       input.ObjectValue,
		EvidenceVerdict:   input.EvidenceVerdict,
		Reason:            "identity_needs_review",
		Payload: map[string]any{
			"error": reason,
		},
	})
}

func insertV2RelationshipReview(
	ctx context.Context,
	tx *gorm.DB,
	commit V2CommitPlacementSemanticInput,
	input V2PlacementRelationshipReviewInput,
) (*V2RelationshipDecisionResult, error) {
	objectRef := input.ObjectRef
	objectValueID := ""
	if input.ObjectValue != nil {
		value, err := upsertV2PlacementValue(ctx, tx, commit, *input.ObjectValue)
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
	observationID, err := insertV2RelationshipObservation(ctx, tx, V2ApplyRelationshipDecisionInput{
		TeamID:              commit.TeamID,
		OwnerProfileID:      commit.OwnerProfileID,
		IngestID:            commit.IngestID,
		PlacementItemID:     commit.PlacementItemID,
		ProposalRef:         input.Ref,
		SubjectRef:          input.SubjectRef,
		OriginalPredicate:   input.OriginalPredicate,
		ObjectRef:           objectRef,
		ObjectValueID:       objectValueID,
		EvidenceVerdict:     input.EvidenceVerdict,
		ObservationMetadata: observationMetadata,
	}, "")
	if err != nil {
		return nil, err
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
	payloadJSON, err := marshalV2JSON(payload)
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
	return &V2RelationshipDecisionResult{
		ObservationID:  observationID,
		ReviewTaskID:   taskID,
		ProposalID:     input.Ref,
		OwnerProfileID: commit.OwnerProfileID,
		Category:       taskType,
		Reason:         reason,
	}, rows.Err()
}
