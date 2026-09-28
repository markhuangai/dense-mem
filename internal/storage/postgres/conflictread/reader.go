package conflictread

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

func LoadRelationshipConflictRecordsInSpace(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipIDs []string,
	knownAt *time.Time,
	spaceID string,
) ([]conflictcontract.RelationshipConflictCaseRecord, error) {
	relationshipIDs = normalizeConflictUUIDList(relationshipIDs)
	if len(relationshipIDs) == 0 {
		return []conflictcontract.RelationshipConflictCaseRecord{}, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT DISTINCT conflict.conflict_id::text
		FROM relationship_conflict_cases AS conflict
		JOIN relationship_conflict_position_members AS member
		  ON member.team_id = conflict.team_id
		 AND member.conflict_id = conflict.conflict_id
		WHERE conflict.team_id = ?::uuid
		  AND member.relationship_id = ANY(?::uuid[])
		  AND (
		      ? = ''
		      OR (
		          conflict.space_id = NULLIF(?, '')::uuid
		          AND member.space_id = NULLIF(?, '')::uuid
		      )
		  )
		  AND (?::timestamptz IS NULL OR conflict.created_at <= ?::timestamptz)
		  AND (
		      (?::timestamptz IS NULL AND member.active)
		      OR (
		          ?::timestamptz IS NOT NULL
		          AND member.first_seen_at <= ?::timestamptz
		          AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
		      )
		  )
		  AND (
		      (?::timestamptz IS NULL AND conflict.status IN ('open', 'overdue', 'resolved'))
		      OR (?::timestamptz IS NOT NULL AND conflict.status IN ('open', 'overdue', 'resolved', 'dismissed'))
		  )
		ORDER BY conflict.conflict_id::text
	`, teamID, pq.Array(relationshipIDs), spaceID, spaceID, spaceID,
		knownAt, knownAt, knownAt, knownAt, knownAt, knownAt, knownAt, knownAt).Rows()
	if err != nil {
		return nil, err
	}
	conflictIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		conflictIDs = append(conflictIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return LoadRelationshipConflictRecordsByID(ctx, tx, teamID, conflictIDs, knownAt)
}

func LoadRelationshipConflictRecordsByID(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
) ([]conflictcontract.RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBounded(ctx, tx, teamID, conflictIDs, knownAt, 0, SupporterLimit)
}

func loadRelationshipConflictRecordsByIDBounded(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	supporterLimit int,
) ([]conflictcontract.RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsByIDBoundedWithFence(ctx, tx, teamID, conflictIDs, knownAt, positionLimit, supporterLimit, false)
}

func loadRelationshipConflictPositionRowsWithLimitAndFence(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positionLimit int,
	activeOnly bool,
) ([]conflictcontract.RelationshipConflictPositionRecord, error) {
	dispositionSelect := "position.disposition"
	dispositionGroup := ", position.disposition"
	if knownAt != nil {
		dispositionSelect = "'candidate'"
		dispositionGroup = ""
	}
	activeMemberFence := ""
	activePositionFence := ""
	if activeOnly {
		activeMemberFence = "\n\t\t\t AND " + storagepostgres.ActiveSemanticSpaceGenerationSQL("member")
		activePositionFence = "\n\t\t\t  AND " + storagepostgres.ActiveSemanticSpaceGenerationSQL("position")
	}
	rows, err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		WITH grouped AS (
			SELECT position.conflict_id::text AS conflict_id,
			       position.position_id::text AS position_id,
			       position.position_key,
			       COALESCE(position.object_entity_id::text, '') AS object_entity_id,
			       COALESCE(position.object_value_id::text, '') AS object_value_id,
			       %s AS disposition,
			       COALESCE(array_remove(array_agg(DISTINCT member.relationship_id::text ORDER BY member.relationship_id::text), NULL), ARRAY[]::text[]) AS relationship_ids,
			       COALESCE(array_remove(array_agg(DISTINCT member.owner_profile_id::text ORDER BY member.owner_profile_id::text), NULL), ARRAY[]::text[]) AS owner_profile_ids,
			       COALESCE(array_remove(array_agg(DISTINCT member.fragment_id::text ORDER BY member.fragment_id::text), NULL), ARRAY[]::text[]) AS evidence_ids,
			       max(member.effective_at) AS effective_at,
			       max(member.effective_time_basis) AS effective_time_basis,
			       COALESCE(bool_and(member.recorded_fallback), false) AS recorded_fallback
			FROM relationship_conflict_positions AS position
			LEFT JOIN relationship_conflict_position_members AS member
			 ON member.team_id = position.team_id
			 AND member.position_id = position.position_id
				 AND (
				     (?::timestamptz IS NULL AND member.active)
				     OR (
			         ?::timestamptz IS NOT NULL
			         AND member.first_seen_at <= ?::timestamptz
				         AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
				     )
				 )
				 %s
			WHERE position.team_id = ?::uuid
			  AND position.conflict_id = ANY(?::uuid[])
			  AND (
			      (?::timestamptz IS NULL AND position.active)
			      OR (
			          ?::timestamptz IS NOT NULL
			          AND position.first_seen_at <= ?::timestamptz
			          AND (position.retired_at IS NULL OR position.retired_at > ?::timestamptz)
			      )
			  )
			  %s
			GROUP BY position.conflict_id, position.position_id, position.position_key,
			         position.object_entity_id, position.object_value_id%s
		), ranked AS (
			SELECT grouped.*,
			       COUNT(*) OVER (PARTITION BY conflict_id)::int AS position_count,
			       row_number() OVER (PARTITION BY conflict_id ORDER BY position_key, position_id) AS position_rank
			FROM grouped
		)
		SELECT conflict_id, position_id, position_key, object_entity_id, object_value_id,
		       disposition,
		       relationship_ids, owner_profile_ids, evidence_ids, effective_at,
		       effective_time_basis, recorded_fallback, position_count
		FROM ranked
		WHERE ?::int <= 0 OR position_rank <= ?::int
		ORDER BY conflict_id, position_key, position_id
		`, dispositionSelect, activeMemberFence, activePositionFence, dispositionGroup),
		knownAt, knownAt, knownAt, knownAt,
		teamID, pq.Array(conflictIDs),
		knownAt, knownAt, knownAt, knownAt,
		positionLimit, positionLimit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []conflictcontract.RelationshipConflictPositionRecord{}
	for rows.Next() {
		var relationshipIDs, ownerProfileIDs, evidenceIDs pq.StringArray
		var record conflictcontract.RelationshipConflictPositionRecord
		if err := rows.Scan(
			&record.ConflictID,
			&record.PositionID,
			&record.PositionKey,
			&record.ObjectEntityID,
			&record.ObjectValueID,
			&record.Disposition,
			&relationshipIDs,
			&ownerProfileIDs,
			&evidenceIDs,
			&record.EffectiveAt,
			&record.EffectiveTimeBasis,
			&record.RecordedFallback,
			&record.PositionCount,
		); err != nil {
			return nil, err
		}
		record.RelationshipIDs = []string(relationshipIDs)
		record.OwnerProfileIDs = []string(ownerProfileIDs)
		record.EvidenceIDs = []string(evidenceIDs)
		record.PositionsTruncated = positionLimit > 0 && record.PositionCount > positionLimit
		out = append(out, record)
	}
	return out, rows.Err()
}

func positionsForConflict(
	conflictID string,
	positions []conflictcontract.RelationshipConflictPositionRecord,
) []conflictcontract.RelationshipConflictPositionRecord {
	out := []conflictcontract.RelationshipConflictPositionRecord{}
	for _, position := range positions {
		if position.ConflictID == conflictID {
			out = append(out, position)
		}
	}
	return out
}

func normalizeConflictUUIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if _, err := uuid.Parse(value); err != nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
