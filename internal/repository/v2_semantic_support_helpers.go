package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func insertV2SupportDecision(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, supportID, relationshipID, decision, reason string) (string, error) {
	return insertV2SupportDecisionEvent(ctx, tx, v2SupportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		SupportID:      supportID,
		RelationshipID: relationshipID,
		Decision:       decision,
		Reason:         reason,
	})
}

type v2SupportDecisionInput struct {
	TeamID         string
	OwnerProfileID string
	ActorProfileID string
	SupportID      string
	RelationshipID string
	Decision       string
	Reason         string
	IdempotencyKey string
	Metadata       map[string]any
}

func insertV2SupportDecisionEvent(ctx context.Context, tx *gorm.DB, input v2SupportDecisionInput) (string, error) {
	metadata, err := marshalV2JSON(input.Metadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_support_decision_events (
		    team_id, support_id, relationship_id, owner_profile_id, actor_profile_id,
		    decision, reason, idempotency_key, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb
		)
		RETURNING support_decision_id::text
	`, input.TeamID, input.SupportID, input.RelationshipID, input.OwnerProfileID, v2SupportDecisionActorProfileID(input),
		input.Decision, input.Reason, input.IdempotencyKey, string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var decisionID string
	if err := rows.Scan(&decisionID); err != nil {
		return "", err
	}
	return decisionID, rows.Err()
}

func v2SupportDecisionActorProfileID(input v2SupportDecisionInput) string {
	if input.ActorProfileID != "" {
		return input.ActorProfileID
	}
	return input.OwnerProfileID
}

func refreshV2RelationshipSupportCounts(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) error {
	_, err := recomputeV2RelationshipFromEffectiveSupport(ctx, tx, teamID, relationshipID, "", "support_recomputed")
	return err
}

type v2RelationshipSupportRecomputeResult struct {
	Before            *V2RelationshipRecord
	After             *V2RelationshipRecord
	SupportDecisionID string
	TransitionID      string
}

func recomputeV2RelationshipFromEffectiveSupport(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	supportDecisionID string,
	reason string,
) (*v2RelationshipSupportRecomputeResult, error) {
	before, err := loadV2RelationshipRecordForUpdate(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	counts, err := effectiveV2RelationshipSupportCounts(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	nextTier, nextStatus := v2TierStatusForEffectiveSupport(before.Tier, before.Status, counts.SupportCount, counts.SourceGroupCount, counts.HasAuthoritative)
	versionBump := `
		CASE
			WHEN support_count <> ?
			  OR source_group_count <> ?
			  OR tier <> ?
			  OR status <> ?
			THEN 1 ELSE 0 END`
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_records
		SET support_count = ?,
		    source_group_count = ?,
		    tier = ?,
		    status = ?,
		    version = version + `+versionBump+`,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND owner_profile_id = ?::uuid
	`, counts.SupportCount, counts.SourceGroupCount, nextTier, nextStatus,
		counts.SupportCount, counts.SourceGroupCount, nextTier, nextStatus,
		teamID, relationshipID, before.OwnerProfileID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrV2SemanticOwnerMismatch
	}
	after, err := loadV2RelationshipRecord(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	out := &v2RelationshipSupportRecomputeResult{
		Before:            before,
		After:             after,
		SupportDecisionID: supportDecisionID,
	}
	if before.Tier == after.Tier && before.Status == after.Status {
		return out, nil
	}
	transitionID, err := insertV2RelationshipTransition(ctx, tx, v2TransitionInput{
		TeamID:            teamID,
		OwnerProfileID:    before.OwnerProfileID,
		RelationshipID:    relationshipID,
		FromTier:          before.Tier,
		FromStatus:        before.Status,
		ToTier:            after.Tier,
		ToStatus:          after.Status,
		Reason:            reason,
		SupportDecisionID: supportDecisionID,
	})
	if err != nil {
		return nil, err
	}
	out.TransitionID = transitionID
	return out, nil
}

type v2EffectiveSupportCounts struct {
	SupportCount     int
	SourceGroupCount int
	HasAuthoritative bool
}

func effectiveV2RelationshipSupportCounts(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (v2EffectiveSupportCounts, error) {
	row := tx.WithContext(ctx).Raw(`
		WITH latest AS (
			SELECT DISTINCT ON (support_id)
			       support_id,
			       decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			ORDER BY support_id, created_at DESC, support_decision_id DESC
		),
		counts AS (
			SELECT COUNT(*)::int AS support_count,
			       COUNT(DISTINCT support.source_group_key)::int AS source_group_count,
			       COALESCE(bool_or(support.authority = 'authoritative'), false) AS has_authoritative
			FROM relationship_evidence_supports AS support
			JOIN latest
			  ON latest.support_id = support.support_id
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = support.team_id
			 AND quarantine.fragment_id = support.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			WHERE support.team_id = ?::uuid
			  AND support.relationship_id = ?::uuid
			  AND latest.decision IN ('grant', 'reinstate')
			  AND quarantine.quarantine_id IS NULL
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
		)
		SELECT support_count, source_group_count, has_authoritative
		FROM counts
	`, teamID, relationshipID, teamID, relationshipID).Row()
	var counts v2EffectiveSupportCounts
	if err := row.Scan(&counts.SupportCount, &counts.SourceGroupCount, &counts.HasAuthoritative); err != nil {
		return v2EffectiveSupportCounts{}, err
	}
	return counts, nil
}

func v2TierStatusForEffectiveSupport(currentTier, currentStatus string, supportCount, sourceGroupCount int, authoritative bool) (string, string) {
	if !v2RelationshipStatusAllowsSupportRecompute(currentStatus) {
		return currentTier, currentStatus
	}
	if supportCount == 0 {
		return string(domain.V2RelationshipTierCandidate), string(domain.V2RelationshipStatusPendingEvidence)
	}
	if authoritative || sourceGroupCount >= 2 {
		return string(domain.V2RelationshipTierFact), string(domain.V2RelationshipStatusActive)
	}
	return string(domain.V2RelationshipTierValidatedClaim), string(domain.V2RelationshipStatusActive)
}

func v2RelationshipStatusAllowsSupportRecompute(status string) bool {
	switch status {
	case string(domain.V2RelationshipStatusActive), string(domain.V2RelationshipStatusPendingEvidence):
		return true
	default:
		return false
	}
}
