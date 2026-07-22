package repository

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func supersedeV2OneCardinalityRelationships(
	ctx context.Context,
	tx *gorm.DB,
	input V2ApplyRelationshipDecisionInput,
	keepRelationshipID string,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH selected AS (
			SELECT relationship_id, tier, status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND subject_entity_id = ?::uuid
			  AND predicate_key = ?
			  AND polarity = ?
			  AND valid_from IS NOT DISTINCT FROM ?
			  AND valid_to IS NOT DISTINCT FROM ?
			  AND scope_key IS NOT DISTINCT FROM NULLIF(?, '')
			  AND relationship_id <> COALESCE(NULLIF(?, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
			  AND (
			      predicate_version < ?
			      OR (predicate_version = ? AND current_cardinality = 'one')
			  )
			  AND status = 'active'
			  AND tier IN ('validated_claim', 'fact')
			FOR UPDATE
		),
		updated AS (
			UPDATE relationship_records AS relationship
			SET status = 'superseded',
			    version = relationship.version + 1,
			    updated_at = now(),
			    recorded_to = now()
			FROM selected
			WHERE relationship.team_id = ?::uuid
			  AND relationship.relationship_id = selected.relationship_id
			RETURNING selected.relationship_id::text, selected.tier, selected.status
		)
		SELECT relationship_id, tier, status
		FROM updated
	`, input.TeamID, input.OwnerProfileID, input.SubjectEntityID, input.PredicateKey,
		input.Polarity, v2TimeArg(input.ValidFrom), v2TimeArg(input.ValidTo), input.ScopeKey,
		keepRelationshipID, input.PredicateVersion, input.PredicateVersion, input.TeamID).Rows()
	if err != nil {
		return err
	}
	type supersededRelationship struct {
		relationshipID string
		tier           string
		status         string
	}
	var superseded []supersededRelationship
	for rows.Next() {
		var relationshipID, tier, status string
		if err := rows.Scan(&relationshipID, &tier, &status); err != nil {
			_ = rows.Close()
			return err
		}
		superseded = append(superseded, supersededRelationship{
			relationshipID: relationshipID,
			tier:           tier,
			status:         status,
		})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range superseded {
		if _, err := insertV2RelationshipTransition(ctx, tx, v2TransitionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			RelationshipID: item.relationshipID,
			FromTier:       item.tier,
			FromStatus:     item.status,
			ToTier:         item.tier,
			ToStatus:       string(domain.V2RelationshipStatusSuperseded),
			Reason:         "one_cardinality_replaced",
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateV2SupportOwnership(ctx context.Context, tx *gorm.DB, input V2ApplyRelationshipDecisionInput) error {
	if input.Support == nil {
		return nil
	}
	if (input.Support.SourceID == "") != (input.Support.SourceRevisionID == "") {
		return errors.New("support source and source revision must be provided together")
	}
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM evidence_fragments AS fragment
			WHERE fragment.team_id = ?::uuid
			  AND fragment.ingest_id = ?::uuid
			  AND fragment.fragment_id = ?::uuid
			  AND fragment.owner_profile_id = ?::uuid
	`
	args := []any{input.TeamID, input.IngestID, input.Support.FragmentID, input.OwnerProfileID}
	if input.Support.SourceID != "" {
		query += `
			  AND fragment.source_id = ?::uuid
			  AND fragment.source_revision_id = ?::uuid
			  AND EXISTS (
			      SELECT 1
			      FROM evidence_source_revisions AS revision
			      WHERE revision.team_id = fragment.team_id
			        AND revision.source_id = fragment.source_id
			        AND revision.source_revision_id = fragment.source_revision_id
			        AND revision.owner_profile_id = fragment.owner_profile_id
			  )
		`
		args = append(args, input.Support.SourceID, input.Support.SourceRevisionID)
	}
	query += `
		)
	`
	ok, err := existsV2OwnerReference(ctx, tx, query, args...)
	if err != nil {
		return err
	}
	if !ok {
		return ErrV2SemanticOwnerMismatch
	}
	return nil
}

func existsV2OwnerReference(ctx context.Context, tx *gorm.DB, query string, args ...any) (bool, error) {
	var exists bool
	if err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func requireV2RelationshipVersion(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	ownerProfileID string,
	version int,
) error {
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND version = ?
	`
	args := []any{teamID, relationshipID, version}
	if ownerProfileID != "" {
		query += `
			  AND owner_profile_id = ?::uuid
		`
		args = append(args, ownerProfileID)
	}
	query += `
		)
	`
	exists, err := existsV2OwnerReference(ctx, tx, query, args...)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("relationship version does not match current relationship")
	}
	return nil
}

func requireV2VerificationForRelationship(ctx context.Context, tx *gorm.DB, teamID, verificationEventID, ownerProfileID, relationshipID string) error {
	exists, err := existsV2OwnerReference(ctx, tx, `
		SELECT EXISTS (
			SELECT 1
			FROM verification_events AS event
			JOIN relationship_observations AS observation
			  ON observation.team_id = event.team_id
			 AND observation.observation_id = event.observation_id
			 AND observation.owner_profile_id = event.owner_profile_id
			WHERE event.team_id = ?::uuid
			  AND event.verification_event_id = ?::uuid
			  AND event.owner_profile_id = ?::uuid
			  AND observation.relationship_id = ?::uuid
		)
	`, teamID, verificationEventID, ownerProfileID, relationshipID)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("verification event does not match source relationship")
	}
	return nil
}

func selectV2RelationshipSupport(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	ownerProfileID string,
	fragmentID string,
	spanStart int,
	spanEnd int,
) (string, error) {
	var supportID string
	row := tx.WithContext(ctx).Raw(`
		SELECT support_id::text
		FROM relationship_evidence_supports
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND fragment_id = ?::uuid
		  AND span_start = ?
		  AND span_end = ?
	`, teamID, relationshipID, ownerProfileID, fragmentID, spanStart, spanEnd).Row()
	if err := row.Scan(&supportID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", gorm.ErrRecordNotFound
		}
		return "", err
	}
	if supportID == "" {
		return "", gorm.ErrRecordNotFound
	}
	return supportID, nil
}

func insertV2RelationshipSupport(
	ctx context.Context,
	tx *gorm.DB,
	input V2ApplyRelationshipDecisionInput,
	relationshipID string,
	observationID string,
	verificationID string,
) (string, string, error) {
	metadata, err := marshalV2JSON(input.Support.Metadata)
	if err != nil {
		return "", "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
			INSERT INTO relationship_evidence_supports (
			    team_id, relationship_id, observation_id, verification_event_id,
			    fragment_id, owner_profile_id, source_group_key, source_id,
			    source_revision_id, span_start, span_end, quote, authority, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, ?::jsonb
			)
			ON CONFLICT ON CONSTRAINT relationship_supports_identity_unique DO NOTHING
			RETURNING support_id::text, true AS created
		)
		SELECT support_id, created FROM inserted
		UNION ALL
		SELECT support_id::text, false AS created
		FROM relationship_evidence_supports
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND fragment_id = ?::uuid
		  AND span_start = ?
		  AND span_end = ?
		LIMIT 1
	`, input.TeamID, relationshipID, observationID, verificationID,
		input.Support.FragmentID, input.OwnerProfileID, input.Support.SourceGroupKey,
		input.Support.SourceID, input.Support.SourceRevisionID, input.Support.SpanStart,
		input.Support.SpanEnd, input.Support.Quote, input.Support.Authority, string(metadata),
		input.TeamID, relationshipID, input.OwnerProfileID, input.Support.FragmentID, input.Support.SpanStart,
		input.Support.SpanEnd).Rows()
	if err != nil {
		return "", "", err
	}
	if !rows.Next() {
		err := rows.Err()
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			return "", "", err
		}
		supportID, err := selectV2RelationshipSupport(ctx, tx, input.TeamID, relationshipID, input.OwnerProfileID, input.Support.FragmentID, input.Support.SpanStart, input.Support.SpanEnd)
		if err != nil {
			return "", "", err
		}
		return supportID, "", nil
	}
	var supportID string
	var created bool
	if err := rows.Scan(&supportID, &created); err != nil {
		_ = rows.Close()
		return "", "", err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", "", err
	}
	if err := rows.Close(); err != nil {
		return "", "", err
	}
	if supportID == "" {
		supportID, err = selectV2RelationshipSupport(ctx, tx, input.TeamID, relationshipID, input.OwnerProfileID, input.Support.FragmentID, input.Support.SpanStart, input.Support.SpanEnd)
		if err != nil {
			return "", "", err
		}
	}
	if !created {
		return supportID, "", nil
	}
	decisionID, err := insertV2SupportDecision(ctx, tx, input.TeamID, input.OwnerProfileID, supportID, relationshipID, "grant", "verifier_entailed")
	if err != nil {
		return "", "", err
	}
	return supportID, decisionID, nil
}

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
		IdempotencyKey:    v2RelationshipTransitionIdempotencyKey("", supportDecisionID),
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
