package repository

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var errRelationshipVersionMismatch = errors.New("relationship version does not match current relationship")

func supersedeOneCardinalityRelationships(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
	keepRelationshipID string,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH selected AS (
			SELECT relationship_id, status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND subject_entity_id = ?::uuid
			  AND predicate_key = ?
			  AND polarity = ?
			  AND valid_from IS NOT DISTINCT FROM ?
			  AND scope_key IS NOT DISTINCT FROM NULLIF(?, '')
			  AND identity_alias_of_relationship_id IS NULL
			  AND relationship_id <> COALESCE(NULLIF(?, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
			  AND (
			      predicate_version < ?
			      OR (predicate_version = ? AND current_cardinality = 'one')
			  )
			  AND status = 'active'
			  AND support_count > 0
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
			RETURNING selected.relationship_id::text, selected.status
		)
		SELECT relationship_id, status
		FROM updated
	`, input.TeamID, input.OwnerProfileID, input.SubjectEntityID, input.PredicateKey,
		input.Polarity, timeArg(input.ValidFrom), input.ScopeKey,
		keepRelationshipID, input.PredicateVersion, input.PredicateVersion, input.TeamID).Rows()
	if err != nil {
		return err
	}
	type supersededRelationship struct {
		relationshipID string
		status         string
	}
	var superseded []supersededRelationship
	for rows.Next() {
		var relationshipID, status string
		if err := rows.Scan(&relationshipID, &status); err != nil {
			_ = rows.Close()
			return err
		}
		superseded = append(superseded, supersededRelationship{
			relationshipID: relationshipID,
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
		if _, err := insertRelationshipTransition(ctx, tx, transitionInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
			RelationshipID: item.relationshipID,
			FromStatus:     item.status,
			ToStatus:       string(domain.RelationshipStatusSuperseded),
			Reason:         "one_cardinality_replaced",
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateSupportOwnership(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput) error {
	spaceID, err := loadSemanticInputSpaceID(ctx, tx, input)
	if err != nil {
		return err
	}
	for _, support := range relationshipEvidenceSupports(input.Support, input.Supports) {
		if err := validateSingleSupportOwnership(ctx, tx, input, support, spaceID); err != nil {
			return err
		}
	}
	return nil
}

func validateSingleSupportOwnership(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput, support EvidenceSupportInput, spaceID string) error {
	if (support.SourceID == "") != (support.SourceRevisionID == "") {
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
			  AND fragment.space_id = ?::uuid
	`
	args := []any{input.TeamID, input.IngestID, support.FragmentID, input.OwnerProfileID, spaceID}
	if support.SourceID != "" {
		query += `
			  AND (
			      (
			          fragment.source_id = ?::uuid
			          AND fragment.source_revision_id = ?::uuid
			          AND EXISTS (
			              SELECT 1
			              FROM evidence_source_revisions AS revision
			              WHERE revision.team_id = fragment.team_id
			                AND revision.source_id = fragment.source_id
			                AND revision.source_revision_id = fragment.source_revision_id
			                AND revision.owner_profile_id = fragment.owner_profile_id
			          )
			      )
			      OR (
			          fragment.source_id IS NULL
			          AND fragment.source_revision_id IS NULL
			          AND EXISTS (
			              SELECT 1
			              FROM remember_source_revision_intents AS intent
			              JOIN evidence_source_revisions AS revision
			                ON revision.team_id = intent.team_id
			               AND revision.source_id = intent.source_id
			               AND revision.source_revision_id = intent.source_revision_id
			               AND revision.owner_profile_id = intent.owner_profile_id
			              WHERE intent.team_id = fragment.team_id
			                AND intent.owner_profile_id = fragment.owner_profile_id
			                AND intent.ingest_id = fragment.ingest_id
			                AND intent.fragment_id = fragment.fragment_id
			                AND intent.source_id = ?::uuid
			                AND intent.source_revision_id = ?::uuid
			          )
			      )
			  )
		`
		args = append(args, support.SourceID, support.SourceRevisionID, support.SourceID, support.SourceRevisionID)
	}
	query += `
		)
	`
	ok, err := existsOwnerReference(ctx, tx, query, args...)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSemanticOwnerMismatch
	}
	return nil
}

func existsOwnerReference(ctx context.Context, tx *gorm.DB, query string, args ...any) (bool, error) {
	var exists bool
	if err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func requireRelationshipVersion(
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
	exists, err := existsOwnerReference(ctx, tx, query, args...)
	if err != nil {
		return err
	}
	if !exists {
		return errRelationshipVersionMismatch
	}
	return nil
}

func loadRelationshipSpaceID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	version int,
) (string, error) {
	var spaceID string
	err := tx.WithContext(ctx).Raw(`
		SELECT space_id::text
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND version = ?
	`, teamID, relationshipID, version).Row().Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("relationship version does not have a memory space")
	}
	return spaceID, err
}

func requireVerificationForRelationship(ctx context.Context, tx *gorm.DB, teamID, verificationEventID, ownerProfileID, relationshipID string) error {
	exists, err := existsOwnerReference(ctx, tx, `
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

func selectRelationshipSupport(
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

func insertRelationshipSupport(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
	relationshipID string,
	observationID string,
	verificationID string,
) (string, string, error) {
	metadata, err := marshalJSON(input.Support.Metadata)
	if err != nil {
		return "", "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
			INSERT INTO relationship_evidence_supports (
			    team_id, relationship_id, observation_id, verification_event_id,
			    fragment_id, owner_profile_id, source_group_key, source_id,
			    source_revision_id, span_start, span_end, quote, authority, metadata, space_id
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?, ?, ?, ?::jsonb,
			(SELECT relationship.space_id
			 FROM relationship_records AS relationship
			 WHERE relationship.team_id = ?::uuid
			   AND relationship.relationship_id = ?::uuid)
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
		input.TeamID, relationshipID,
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
		supportID, err := selectRelationshipSupport(ctx, tx, input.TeamID, relationshipID, input.OwnerProfileID, input.Support.FragmentID, input.Support.SpanStart, input.Support.SpanEnd)
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
		supportID, err = selectRelationshipSupport(ctx, tx, input.TeamID, relationshipID, input.OwnerProfileID, input.Support.FragmentID, input.Support.SpanStart, input.Support.SpanEnd)
		if err != nil {
			return "", "", err
		}
	}
	if !created {
		return supportID, "", nil
	}
	decisionID, err := insertSupportDecision(ctx, tx, input.TeamID, input.OwnerProfileID, supportID, relationshipID, "grant", "verifier_entailed")
	if err != nil {
		return "", "", err
	}
	return supportID, decisionID, nil
}

func insertRelationshipSupports(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
	relationshipID string,
	observationID string,
	verificationID string,
) ([]string, []string, error) {
	supports := relationshipEvidenceSupports(input.Support, input.Supports)
	supportIDs := make([]string, 0, len(supports))
	supportDecisionIDs := make([]string, 0, len(supports))
	for _, support := range supports {
		supportInput := input
		supportCopy := support
		supportInput.Support = &supportCopy
		supportInput.Supports = nil
		supportID, supportDecisionID, err := insertRelationshipSupport(ctx, tx, supportInput, relationshipID, observationID, verificationID)
		if err != nil {
			return nil, nil, err
		}
		supportIDs = append(supportIDs, supportID)
		if supportDecisionID != "" {
			supportDecisionIDs = append(supportDecisionIDs, supportDecisionID)
		}
	}
	return supportIDs, supportDecisionIDs, nil
}

func insertSupportDecision(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, supportID, relationshipID, decision, reason string) (string, error) {
	return insertSupportDecisionEvent(ctx, tx, supportDecisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		SupportID:      supportID,
		RelationshipID: relationshipID,
		Decision:       decision,
		Reason:         reason,
	})
}

type supportDecisionInput struct {
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

func insertSupportDecisionEvent(ctx context.Context, tx *gorm.DB, input supportDecisionInput) (string, error) {
	metadata, err := marshalJSON(input.Metadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_support_decision_events (
		    team_id, support_id, relationship_id, owner_profile_id, actor_profile_id,
		    decision, reason, idempotency_key, metadata, space_id
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb,
			(SELECT relationship.space_id
			 FROM relationship_records AS relationship
			 WHERE relationship.team_id = ?::uuid
			   AND relationship.relationship_id = ?::uuid)
		)
		RETURNING support_decision_id::text
	`, input.TeamID, input.SupportID, input.RelationshipID, input.OwnerProfileID, supportDecisionActorProfileID(input),
		input.Decision, input.Reason, input.IdempotencyKey, string(metadata),
		input.TeamID, input.RelationshipID).Rows()
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

func supportDecisionActorProfileID(input supportDecisionInput) string {
	if input.ActorProfileID != "" {
		return input.ActorProfileID
	}
	return input.OwnerProfileID
}

func refreshRelationshipSupportCounts(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) error {
	_, err := recomputeRelationshipFromEffectiveSupport(ctx, tx, teamID, relationshipID, "", "support_recomputed")
	return err
}

type relationshipSupportRecomputeResult struct {
	Before            *RelationshipRecord
	After             *RelationshipRecord
	SupportDecisionID string
	TransitionID      string
}

func recomputeRelationshipFromEffectiveSupport(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	supportDecisionID string,
	reason string,
) (*relationshipSupportRecomputeResult, error) {
	before, err := loadRelationshipRecordForUpdate(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	if before.IdentityAliasOfID != "" {
		return nil, ErrSemanticIdentityAlias
	}
	counts, err := effectiveRelationshipSupportCounts(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	nextStatus := statusForEffectiveSupport(before.Status, counts.SupportCount)
	versionBump := `
		CASE
			WHEN support_count <> ?
			  OR source_group_count <> ?
			  OR status <> ?
			THEN 1 ELSE 0 END`
	result := tx.WithContext(ctx).Exec(`
		UPDATE relationship_records
		SET support_count = ?,
		    source_group_count = ?,
		    status = ?,
		    version = version + `+versionBump+`,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND owner_profile_id = ?::uuid
	`, counts.SupportCount, counts.SourceGroupCount, nextStatus,
		counts.SupportCount, counts.SourceGroupCount, nextStatus,
		teamID, relationshipID, before.OwnerProfileID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrSemanticOwnerMismatch
	}
	after, err := loadRelationshipRecord(ctx, tx, teamID, relationshipID)
	if err != nil {
		return nil, err
	}
	out := &relationshipSupportRecomputeResult{
		Before:            before,
		After:             after,
		SupportDecisionID: supportDecisionID,
	}
	if before.Status == after.Status {
		return out, nil
	}
	transitionID, err := insertRelationshipTransition(ctx, tx, transitionInput{
		TeamID:            teamID,
		OwnerProfileID:    before.OwnerProfileID,
		RelationshipID:    relationshipID,
		FromStatus:        before.Status,
		ToStatus:          after.Status,
		Reason:            reason,
		SupportDecisionID: supportDecisionID,
		IdempotencyKey:    relationshipTransitionIdempotencyKey("", supportDecisionID),
	})
	if err != nil {
		return nil, err
	}
	out.TransitionID = transitionID
	return out, nil
}

type effectiveSupportCounts struct {
	SupportCount     int
	SourceGroupCount int
	HasAuthoritative bool
}

func effectiveRelationshipSupportCounts(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (effectiveSupportCounts, error) {
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
		LEFT JOIN evidence_lifecycle_events AS lifecycle
		  ON lifecycle.team_id = support.team_id
		 AND lifecycle.target_fragment_id = support.fragment_id
		WHERE support.team_id = ?::uuid
			  AND support.relationship_id = ?::uuid
		  AND latest.decision IN ('grant', 'reinstate')
		  AND quarantine.quarantine_id IS NULL
		  AND lifecycle.lifecycle_event_id IS NULL
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
		)
		SELECT support_count, source_group_count, has_authoritative
		FROM counts
	`, teamID, relationshipID, teamID, relationshipID).Row()
	var counts effectiveSupportCounts
	if err := row.Scan(&counts.SupportCount, &counts.SourceGroupCount, &counts.HasAuthoritative); err != nil {
		return effectiveSupportCounts{}, err
	}
	return counts, nil
}

func statusForEffectiveSupport(currentStatus string, supportCount int) string {
	if !relationshipStatusAllowsSupportRecompute(currentStatus) {
		return currentStatus
	}
	if supportCount == 0 {
		return string(domain.RelationshipStatusPendingEvidence)
	}
	return string(domain.RelationshipStatusActive)
}

func relationshipStatusAllowsSupportRecompute(status string) bool {
	switch status {
	case string(domain.RelationshipStatusActive), string(domain.RelationshipStatusPendingEvidence):
		return true
	default:
		return false
	}
}
