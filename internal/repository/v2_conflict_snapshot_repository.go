package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func v2ConflictRowsByPosition(rows []v2ConflictPlacementRow) [][]v2ConflictPlacementRow {
	byKey := map[string][]v2ConflictPlacementRow{}
	keys := []string{}
	for _, row := range rows {
		if _, ok := byKey[row.PositionKey]; !ok {
			keys = append(keys, row.PositionKey)
		}
		byKey[row.PositionKey] = append(byKey[row.PositionKey], row)
	}
	out := make([][]v2ConflictPlacementRow, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out
}

func upsertV2RelationshipConflictPosition(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	rowsForPosition []v2ConflictPlacementRow,
) (bool, error) {
	if len(rowsForPosition) == 0 {
		return false, nil
	}
	first := rowsForPosition[0]
	changed, err := v2RelationshipConflictPositionWouldChange(ctx, tx, teamID, conflictID, first)
	if err != nil {
		return false, err
	}
	var positionID string
	var created bool
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
			INSERT INTO relationship_conflict_positions (
			    team_id, conflict_id, position_key, object_entity_id, object_value_id,
			    support_group_count, authoritative_group_count
			) VALUES (
			    ?::uuid, ?::uuid, ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?
			)
			ON CONFLICT (team_id, conflict_id, position_key)
				DO UPDATE SET support_group_count = EXCLUDED.support_group_count,
				              authoritative_group_count = EXCLUDED.authoritative_group_count,
				              active = true,
				              retired_at = NULL,
				              last_seen_at = now(),
				              updated_at = now()
			RETURNING position_id::text, (xmax = 0) AS created
		)
		SELECT position_id, created FROM inserted
	`, teamID, conflictID, first.PositionKey, first.ObjectEntityID, first.ObjectValueID,
		first.SupportGroupCount, first.AuthoritativeGroupCount).Rows()
	if err != nil {
		return false, err
	}
	if rows.Next() {
		if err := rows.Scan(&positionID, &created); err != nil {
			_ = rows.Close()
			return false, err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if positionID == "" {
		return false, sql.ErrNoRows
	}
	if created {
		if err := appendV2RelationshipConflictEvent(ctx, tx, teamID, conflictID, positionID, "", "", string(domain.V2RelationshipConflictEventPositionAdded), "candidate", "case:"+conflictID+":position:"+positionID+":added", map[string]any{
			"position_key": first.PositionKey,
		}); err != nil {
			return false, err
		}
	}
	for _, member := range rowsForPosition {
		memberChanged, err := upsertV2RelationshipConflictMember(ctx, tx, teamID, conflictID, positionID, member)
		if err != nil {
			return false, err
		}
		changed = changed || memberChanged
	}
	return changed || created, nil
}

func upsertV2RelationshipConflictMember(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	positionID string,
	row v2ConflictPlacementRow,
) (bool, error) {
	changed, err := v2RelationshipConflictMemberWouldChange(ctx, tx, teamID, positionID, row)
	if err != nil {
		return false, err
	}
	metadata, err := marshalV2JSON(map[string]any{
		"source": domain.V2ConflictPolicyVersion,
	})
	if err != nil {
		return false, err
	}
	result := tx.WithContext(ctx).Exec(`
		INSERT INTO relationship_conflict_position_members (
			    team_id, conflict_id, position_id, relationship_id, owner_profile_id,
			    support_id, verification_event_id, fragment_id, source_group_key, authority,
			    effective_at, effective_time_basis, recorded_fallback, active, retired_at, metadata
			) VALUES (
			    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
			    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, ?,
			    ?, ?, ?, true, NULL, ?::jsonb
			)
			ON CONFLICT (team_id, position_id, relationship_id, source_group_key)
			DO UPDATE SET support_id = EXCLUDED.support_id,
			              verification_event_id = EXCLUDED.verification_event_id,
			              fragment_id = EXCLUDED.fragment_id,
			              authority = EXCLUDED.authority,
			              effective_at = EXCLUDED.effective_at,
			              effective_time_basis = EXCLUDED.effective_time_basis,
			              recorded_fallback = EXCLUDED.recorded_fallback,
			              active = true,
			              retired_at = NULL,
			              last_seen_at = now(),
			              metadata = EXCLUDED.metadata
	`, teamID, conflictID, positionID, row.RelationshipID, row.OwnerProfileID,
		row.SupportID, row.VerificationEventID, row.FragmentID, row.SourceGroupKey, row.Authority,
		row.EffectiveAt, row.EffectiveTimeBasis, row.RecordedFallback, string(metadata))
	if result.Error != nil {
		return false, result.Error
	}
	if changed && result.RowsAffected == 1 {
		return true, appendV2RelationshipConflictEvent(ctx, tx, teamID, conflictID, positionID, row.RelationshipID, row.OwnerProfileID, string(domain.V2RelationshipConflictEventMemberAdded), "member", "case:"+conflictID+":relationship:"+row.RelationshipID+":source_group:"+row.SourceGroupKey+":member", map[string]any{
			"source_group_key": row.SourceGroupKey,
			"authority":        row.Authority,
		})
	}
	return changed, nil
}

func refreshV2ExistingRelationshipConflictCaseSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	rows []v2ConflictPlacementRow,
) (bool, error) {
	changed := false
	positions := v2ConflictRowsByPosition(rows)
	for _, positionRows := range positions {
		positionChanged, err := upsertV2RelationshipConflictPosition(ctx, tx, teamID, conflictID, positionRows)
		if err != nil {
			return false, err
		}
		changed = changed || positionChanged
	}
	retired, err := reconcileV2RelationshipConflictSnapshot(ctx, tx, teamID, conflictID, rows)
	if err != nil {
		return false, err
	}
	return changed || retired, nil
}

func v2RelationshipConflictPositionWouldChange(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	row v2ConflictPlacementRow,
) (bool, error) {
	var positionID string
	var supportGroupCount int
	var authoritativeGroupCount int
	var active bool
	var retiredAt sql.NullTime
	scan := tx.WithContext(ctx).Raw(`
		SELECT position_id::text, support_group_count, authoritative_group_count, active, retired_at
		FROM relationship_conflict_positions
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND position_key = ?
	`, teamID, conflictID, row.PositionKey).Row().Scan(
		&positionID,
		&supportGroupCount,
		&authoritativeGroupCount,
		&active,
		&retiredAt,
	)
	if errors.Is(scan, sql.ErrNoRows) {
		return true, nil
	}
	if scan != nil {
		return false, scan
	}
	return supportGroupCount != row.SupportGroupCount ||
		authoritativeGroupCount != row.AuthoritativeGroupCount ||
		!active ||
		retiredAt.Valid, nil
}

func v2RelationshipConflictMemberWouldChange(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	positionID string,
	row v2ConflictPlacementRow,
) (bool, error) {
	var supportID string
	var verificationEventID string
	var fragmentID string
	var authority string
	var effectiveAt sql.NullTime
	var effectiveTimeBasis string
	var recordedFallback bool
	var active bool
	var retiredAt sql.NullTime
	scan := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(support_id::text, ''),
		       COALESCE(verification_event_id::text, ''),
		       COALESCE(fragment_id::text, ''),
		       authority,
		       effective_at,
		       effective_time_basis,
		       recorded_fallback,
		       active,
		       retired_at
		FROM relationship_conflict_position_members
		WHERE team_id = ?::uuid
		  AND position_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND source_group_key = ?
	`, teamID, positionID, row.RelationshipID, row.SourceGroupKey).Row().Scan(
		&supportID,
		&verificationEventID,
		&fragmentID,
		&authority,
		&effectiveAt,
		&effectiveTimeBasis,
		&recordedFallback,
		&active,
		&retiredAt,
	)
	if errors.Is(scan, sql.ErrNoRows) {
		return true, nil
	}
	if scan != nil {
		return false, scan
	}
	return supportID != row.SupportID ||
		verificationEventID != row.VerificationEventID ||
		fragmentID != row.FragmentID ||
		authority != row.Authority ||
		!v2ConflictNullableTimesEqual(effectiveAt, row.EffectiveAt) ||
		effectiveTimeBasis != row.EffectiveTimeBasis ||
		recordedFallback != row.RecordedFallback ||
		!active ||
		retiredAt.Valid, nil
}

func v2ConflictNullableTimesEqual(left sql.NullTime, right *time.Time) bool {
	if !left.Valid && right == nil {
		return true
	}
	if left.Valid != (right != nil) {
		return false
	}
	return left.Time.Equal(right.UTC())
}

func reconcileV2RelationshipConflictSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	rows []v2ConflictPlacementRow,
) (bool, error) {
	positionKeys := make([]string, 0, len(rows))
	relationshipIDs := make([]string, 0, len(rows))
	sourceGroupKeys := make([]string, 0, len(rows))
	seenMembers := map[string]struct{}{}
	for _, row := range rows {
		key := row.PositionKey + "\x00" + row.RelationshipID + "\x00" + row.SourceGroupKey
		if _, ok := seenMembers[key]; ok {
			continue
		}
		seenMembers[key] = struct{}{}
		positionKeys = append(positionKeys, row.PositionKey)
		relationshipIDs = append(relationshipIDs, row.RelationshipID)
		sourceGroupKeys = append(sourceGroupKeys, row.SourceGroupKey)
	}
	var retiredCount int
	err := tx.WithContext(ctx).Raw(`
		WITH snapshot AS (
			SELECT *
			FROM unnest(?::text[], ?::uuid[], ?::text[])
			    AS item(position_key, relationship_id, source_group_key)
		),
		retired_members AS (
			UPDATE relationship_conflict_position_members AS member
			SET active = false,
			    retired_at = COALESCE(member.retired_at, now()),
			    last_seen_at = now()
			FROM relationship_conflict_positions AS position
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.active
			  AND position.team_id = member.team_id
			  AND position.position_id = member.position_id
			  AND NOT EXISTS (
			      SELECT 1
			      FROM snapshot
			      WHERE snapshot.position_key = position.position_key
			        AND snapshot.relationship_id = member.relationship_id
			        AND snapshot.source_group_key = member.source_group_key
			  )
			RETURNING 1
		),
		retired_positions AS (
			UPDATE relationship_conflict_positions AS position
			SET active = false,
			    retired_at = COALESCE(position.retired_at, now()),
			    updated_at = now()
			WHERE position.team_id = ?::uuid
			  AND position.conflict_id = ?::uuid
			  AND position.active
			  AND NOT EXISTS (
			      SELECT 1
			      FROM snapshot
			      WHERE snapshot.position_key = position.position_key
			  )
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM retired_members) + (SELECT count(*) FROM retired_positions)
	`, pq.Array(positionKeys), pq.Array(relationshipIDs), pq.Array(sourceGroupKeys),
		teamID, conflictID, teamID, conflictID).Scan(&retiredCount).Error
	if err != nil {
		return false, err
	}
	return retiredCount > 0, nil
}
