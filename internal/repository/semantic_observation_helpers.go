package repository

import (
	"context"

	"gorm.io/gorm"
)

func insertRelationshipObservation(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput, relationshipID string) (string, error) {
	supports := relationshipEvidenceSupports(input.Support, input.Supports)
	evidence := make([]map[string]any, 0, len(supports))
	for _, support := range supports {
		evidence = append(evidence, map[string]any{
			"fragment_id": support.FragmentID,
			"start":       support.SpanStart,
			"end":         support.SpanEnd,
		})
	}
	evidenceJSON, err := marshalJSONArray(evidence)
	if err != nil {
		return "", err
	}
	metadata, err := marshalJSON(input.ObservationMetadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_observations (
		    team_id, relationship_id, ingest_id, placement_item_id, owner_profile_id,
		    subject_ref, original_predicate, object_ref, subject_entity_id,
		    predicate_key, predicate_version, object_entity_id, object_value_id,
		    polarity, scope_key, valid_from, valid_to, evidence, metadata
		) VALUES (
		    ?::uuid, NULLIF(?, '')::uuid, ?::uuid, NULLIF(?, '')::uuid, ?::uuid,
		    ?, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, ''), NULLIF(?, 0),
		    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, NULLIF(?, ''),
		    ?, ?, ?::jsonb, ?::jsonb
		)
		RETURNING observation_id::text
	`, input.TeamID, relationshipID, input.IngestID, input.PlacementItemID, input.OwnerProfileID,
		input.SubjectRef, input.OriginalPredicate, input.ObjectRef, input.SubjectEntityID,
		input.PredicateKey, input.PredicateVersion, input.ObjectEntityID, input.ObjectValueID,
		input.Polarity, input.ScopeKey, timeArg(input.ValidFrom), timeArg(input.ValidTo), string(evidenceJSON),
		string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var observationID string
	if err := rows.Scan(&observationID); err != nil {
		return "", err
	}
	return observationID, rows.Err()
}

func insertVerificationEvent(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput, observationID string) (string, error) {
	metadata, err := marshalJSON(nil)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO verification_events (
		    team_id, observation_id, owner_profile_id, evidence_verdict,
		    confidence, rationale, model, response_hash, metadata,
		    assessment_id, assessment_policy_version, threshold_used, gate_result
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb,
		    NULLIF(?, '')::uuid, NULLIF(?, ''), ?, NULLIF(?, '')
		)
		RETURNING verification_event_id::text
	`, input.TeamID, observationID, input.OwnerProfileID, input.EvidenceVerdict,
		confidenceArg(input.Confidence), input.Rationale, input.Model, input.ResponseHash,
		string(metadata), input.AssessmentID, input.AssessmentPolicyVersion,
		confidenceArg(input.ThresholdUsed), input.GateResult).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var verificationID string
	if err := rows.Scan(&verificationID); err != nil {
		return "", err
	}
	return verificationID, rows.Err()
}
