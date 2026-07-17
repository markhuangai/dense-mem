package repository

import (
	"context"

	"gorm.io/gorm"
)

func insertV2RelationshipTransition(ctx context.Context, tx *gorm.DB, input v2TransitionInput) (string, error) {
	var transitionID string
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_transition_events (
		    team_id, relationship_id, owner_profile_id, from_tier, from_status,
		    to_tier, to_status, reason, verification_event_id, support_decision_id,
		    idempotency_key
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, ''), NULLIF(?, ''),
		    ?, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?
		)
		RETURNING transition_id::text
	`, input.TeamID, input.RelationshipID, input.OwnerProfileID, input.FromTier,
		input.FromStatus, input.ToTier, input.ToStatus, input.Reason,
		input.VerificationEventID, input.SupportDecisionID, input.IdempotencyKey).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&transitionID); err != nil {
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if transitionID == "" {
		return "", gorm.ErrRecordNotFound
	}
	return transitionID, nil
}
