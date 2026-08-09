package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

const relationshipConflictSupporterLimit = 20

const relationshipConflictSupporterRowsSQL = `
	WITH effective_members AS (
		SELECT member.conflict_id,
		       member.position_id,
		       member.owner_profile_id,
		       member.source_group_key,
		       member.authority,
		       member.fragment_id,
		       member.accepted_at
		FROM relationship_conflict_position_members AS member
		JOIN relationship_conflict_positions AS position
		  ON position.team_id = member.team_id
		 AND position.position_id = member.position_id
		WHERE member.team_id = ?::uuid
		  AND member.conflict_id = ANY(?::uuid[])
		  AND (
		      (?::timestamptz IS NULL AND member.active)
		      OR (
		          ?::timestamptz IS NOT NULL
		          AND member.first_seen_at <= ?::timestamptz
		          AND (member.retired_at IS NULL OR member.retired_at > ?::timestamptz)
		      )
		  )
		  AND (
		      (?::timestamptz IS NULL AND position.active)
		      OR (
		          ?::timestamptz IS NOT NULL
		          AND position.first_seen_at <= ?::timestamptz
		          AND (position.retired_at IS NULL OR position.retired_at > ?::timestamptz)
		      )
		  )
	),
	profile_group_counts AS (
		SELECT conflict_id,
		       position_id,
		       owner_profile_id,
		       COUNT(DISTINCT source_group_key)::int AS source_group_count
		FROM effective_members
		GROUP BY conflict_id, position_id, owner_profile_id
	),
	profile_representatives AS (
		SELECT DISTINCT ON (conflict_id, position_id, owner_profile_id)
		       conflict_id,
		       position_id,
		       owner_profile_id,
		       authority,
		       fragment_id,
		       accepted_at
		FROM effective_members
		ORDER BY conflict_id,
		         position_id,
		         owner_profile_id,
		         CASE authority
		             WHEN 'authoritative' THEN 0
		             WHEN 'primary' THEN 1
		             WHEN 'secondary' THEN 2
		             WHEN 'inferred' THEN 3
		             ELSE 4
		         END,
		         accepted_at DESC,
		         fragment_id
	),
	ranked_supporters AS (
		SELECT representative.conflict_id,
		       representative.position_id,
		       representative.owner_profile_id,
		       profile.name,
		       representative.authority,
		       representative.fragment_id,
		       representative.accepted_at,
		       counts.source_group_count,
		       COUNT(*) OVER (
		           PARTITION BY representative.conflict_id, representative.position_id
		       )::int AS supporter_count,
		       ROW_NUMBER() OVER (
		           PARTITION BY representative.conflict_id, representative.position_id
		           ORDER BY CASE representative.authority
		                        WHEN 'authoritative' THEN 0
		                        WHEN 'primary' THEN 1
		                        WHEN 'secondary' THEN 2
		                        WHEN 'inferred' THEN 3
		                        ELSE 4
		                    END,
		                    representative.accepted_at DESC,
		                    representative.owner_profile_id
		       ) AS supporter_rank
		FROM profile_representatives AS representative
		JOIN profile_group_counts AS counts
		  ON counts.conflict_id = representative.conflict_id
		 AND counts.position_id = representative.position_id
		 AND counts.owner_profile_id = representative.owner_profile_id
		JOIN team_profiles AS profile
		  ON profile.team_id = ?::uuid
		 AND profile.id = representative.owner_profile_id
	)
	SELECT conflict_id::text,
	       position_id::text,
	       supporter_count,
	       owner_profile_id::text,
	       name,
	       authority,
	       fragment_id::text,
	       accepted_at,
	       source_group_count
	FROM ranked_supporters
	WHERE supporter_rank <= ?
	ORDER BY conflict_id, position_id, supporter_rank`

func loadRelationshipConflictSupporters(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positions []RelationshipConflictPositionRecord,
) error {
	if len(positions) == 0 {
		return nil
	}
	rows, err := tx.WithContext(ctx).Raw(
		relationshipConflictSupporterRowsSQL,
		relationshipConflictSupporterRowsArgs(teamID, conflictIDs, knownAt)...,
	).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	positionsByID := make(map[string]*RelationshipConflictPositionRecord, len(positions))
	for i := range positions {
		positions[i].Supporters = []RelationshipConflictSupporterRecord{}
		positionsByID[positions[i].PositionID] = &positions[i]
	}
	for rows.Next() {
		var conflictID, positionID string
		var supporterCount int
		var supporter RelationshipConflictSupporterRecord
		if err := rows.Scan(
			&conflictID,
			&positionID,
			&supporterCount,
			&supporter.ProfileID,
			&supporter.ProfileName,
			&supporter.StrongestAuthority,
			&supporter.EvidenceID,
			&supporter.AcceptedAt,
			&supporter.SourceGroupCount,
		); err != nil {
			return err
		}
		position := positionsByID[positionID]
		if position == nil || position.ConflictID != conflictID {
			return fmt.Errorf("conflict supporter position %s is not in the loaded team projection", positionID)
		}
		position.SupporterCount = supporterCount
		position.Supporters = append(position.Supporters, supporter)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range positions {
		positions[i].SupportersTruncated = positions[i].SupporterCount > len(positions[i].Supporters)
	}
	return nil
}

func relationshipConflictSupporterRowsArgs(teamID string, conflictIDs []string, knownAt *time.Time) []any {
	return []any{
		teamID,
		pq.Array(conflictIDs),
		knownAt, knownAt, knownAt, knownAt,
		knownAt, knownAt, knownAt, knownAt,
		teamID,
		relationshipConflictSupporterLimit,
	}
}
