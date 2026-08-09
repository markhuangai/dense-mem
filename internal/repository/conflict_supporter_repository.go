package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

const relationshipConflictSupporterLimit = 20

// The vote projection deliberately works across every position in a conflict.
// A profile votes only when its newest effective accepted support identifies one
// position. Equal newest timestamps are an abstention, not a position tie-break.
const relationshipConflictSupporterRowsSQL = `
	WITH effective_members AS (
		SELECT member.conflict_id,
		       member.position_id,
		       member.owner_profile_id,
		       member.support_id,
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
	profile_position_latest AS (
		SELECT conflict_id,
		       owner_profile_id,
		       position_id,
		       MAX(accepted_at) AS latest_accepted_at
		FROM effective_members
		GROUP BY conflict_id, owner_profile_id, position_id
	),
	profile_latest AS (
		SELECT conflict_id,
		       owner_profile_id,
		       MAX(latest_accepted_at) AS latest_accepted_at
		FROM profile_position_latest
		GROUP BY conflict_id, owner_profile_id
	),
	profile_latest_positions AS (
		SELECT candidate.conflict_id,
		       candidate.owner_profile_id,
		       candidate.position_id,
		       candidate.latest_accepted_at,
		       COUNT(*) OVER (
		           PARTITION BY candidate.conflict_id, candidate.owner_profile_id
		       ) AS latest_position_count
		FROM profile_position_latest AS candidate
		JOIN profile_latest AS latest
		  ON latest.conflict_id = candidate.conflict_id
		 AND latest.owner_profile_id = candidate.owner_profile_id
		 AND latest.latest_accepted_at = candidate.latest_accepted_at
	),
	voter_positions AS (
		SELECT conflict_id,
		       owner_profile_id,
		       position_id
		FROM profile_latest_positions
		WHERE latest_position_count = 1
	),
	profile_representatives AS (
		SELECT DISTINCT ON (member.conflict_id, member.position_id, member.owner_profile_id)
		       member.conflict_id,
		       member.position_id,
		       member.owner_profile_id,
		       member.authority,
		       member.fragment_id,
		       member.accepted_at,
		       member.support_id
		FROM effective_members AS member
		JOIN voter_positions AS voter
		  ON voter.conflict_id = member.conflict_id
		 AND voter.position_id = member.position_id
		 AND voter.owner_profile_id = member.owner_profile_id
		ORDER BY member.conflict_id,
		         member.position_id,
		         member.owner_profile_id,
		         CASE member.authority
		             WHEN 'authoritative' THEN 0
		             WHEN 'primary' THEN 1
		             WHEN 'secondary' THEN 2
		             WHEN 'inferred' THEN 3
		             ELSE 4
		         END,
		         member.accepted_at DESC,
		         member.support_id,
		         member.fragment_id
	),
	ranked_supporters AS (
		SELECT representative.conflict_id,
		       representative.position_id,
		       representative.owner_profile_id,
		       profile.name,
		       representative.authority,
		       representative.fragment_id,
		       representative.accepted_at,
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
	       accepted_at
	FROM ranked_supporters
	WHERE (cardinality(?::uuid[]) = 0 OR position_id = ANY(?::uuid[]))
	  AND supporter_rank <= ?
	ORDER BY conflict_id, position_id, supporter_rank`

func loadRelationshipConflictSupporters(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictIDs []string,
	knownAt *time.Time,
	positions []RelationshipConflictPositionRecord,
	supporterLimit int,
) error {
	if len(positions) == 0 {
		return nil
	}
	rows, err := tx.WithContext(ctx).Raw(
		relationshipConflictSupporterRowsSQL,
		relationshipConflictSupporterRowsArgsWithLimit(teamID, conflictIDs, positionIDs(positions), knownAt, supporterLimit)...,
	).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()

	positionsByID := make(map[string]*RelationshipConflictPositionRecord, len(positions))
	for i := range positions {
		positions[i].Supporters = []RelationshipConflictSupporterRecord{}
		positions[i].SupporterCount = 0
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
	return relationshipConflictSupporterRowsArgsWithLimit(teamID, conflictIDs, nil, knownAt, relationshipConflictSupporterLimit)
}

func relationshipConflictSupporterRowsArgsWithLimit(teamID string, conflictIDs, positionIDs []string, knownAt *time.Time, supporterLimit int) []any {
	if supporterLimit <= 0 {
		supporterLimit = relationshipConflictSupporterLimit
	}
	if positionIDs == nil {
		positionIDs = []string{}
	}
	return []any{
		teamID,
		pq.Array(conflictIDs),
		knownAt, knownAt, knownAt, knownAt,
		knownAt, knownAt, knownAt, knownAt,
		teamID,
		pq.Array(positionIDs),
		pq.Array(positionIDs),
		supporterLimit,
	}
}

func positionIDs(positions []RelationshipConflictPositionRecord) []string {
	ids := make([]string, 0, len(positions))
	for _, position := range positions {
		ids = append(ids, position.PositionID)
	}
	return ids
}
