package repository

import (
	"context"
	"strconv"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func ensureCommunitySourcesCurrent(ctx context.Context, tx *gorm.DB, teamID string, fence semanticSpaceFence, sources []CommunitySourceInput) error {
	if len(sources) == 0 {
		return nil
	}
	relationshipIDs := make([]string, 0, len(sources))
	versions := make([]int, 0, len(sources))
	for _, source := range sources {
		relationshipIDs = append(relationshipIDs, source.RelationshipID)
		versions = append(versions, source.RelationshipVersion)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH expected AS (
			SELECT *
			FROM unnest(?::uuid[], ?::int[]) AS e(relationship_id, relationship_version)
		), latest_support AS (
			SELECT DISTINCT ON (team_id, support_id) team_id, support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		)
		SELECT expected.relationship_id::text
		FROM expected
		LEFT JOIN relationship_records AS relationship
		  ON relationship.team_id = ?::uuid
		 AND relationship.relationship_id = expected.relationship_id
		 AND relationship.space_id = ?::uuid
		 AND relationship.space_generation = ?
		WHERE relationship.relationship_id IS NULL
		   OR relationship.version <> expected.relationship_version
		   OR relationship.status <> 'active'
		   OR (relationship.object_entity_id IS NULL AND relationship.object_value_id IS NULL)
		   OR NOT EXISTS (
				SELECT 1
				FROM relationship_evidence_supports AS support
				JOIN latest_support AS decision
				  ON decision.team_id = support.team_id
				 AND decision.support_id = support.support_id
				 AND decision.decision IN ('grant', 'reinstate')
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
				  AND support.space_id = ?::uuid
				  AND support.space_generation = ?
				  AND support.relationship_id = expected.relationship_id
				  AND quarantine.quarantine_id IS NULL
				  AND lifecycle.lifecycle_event_id IS NULL
				  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
			)
		LIMIT 1
	`, pq.Array(relationshipIDs), pq.Array(versions), teamID, fence.ID, fence.Generation,
		teamID, fence.ID, fence.Generation, teamID, fence.ID, fence.Generation).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrCommunitySourceStale
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func flattenCommunitySources(communities []CommunityPublishRecord) []CommunitySourceInput {
	seen := map[string]struct{}{}
	out := make([]CommunitySourceInput, 0)
	for _, community := range communities {
		for _, source := range community.Sources {
			key := source.RelationshipID + "\x00" + strconv.Itoa(source.RelationshipVersion)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, source)
		}
	}
	return out
}

func insertCommunityRecord(ctx context.Context, tx *gorm.DB, input CommunitySnapshotPublishInput, fence semanticSpaceFence, community CommunityPublishRecord) error {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO community_records (
			team_id, space_id, space_generation, community_id, run_id, ordinal, status, summary, summary_version,
			logical_community_id, member_count, source_count, top_entities, top_predicates, source_fingerprint,
			summary_input_hash, summary_provider_model, summary_prompt_hash, summary_response_hash, summary_generated_at
		) VALUES (
			?::uuid, ?::uuid, ?, ?::uuid, ?::uuid, ?, 'current', ?, ?, ?::uuid, ?, ?, ?::text[], ?::text[], ?, ?, ?, ?, ?, now()
		)
	`, input.TeamID, fence.ID, fence.Generation, community.CommunityID, input.RunID, community.Ordinal,
		community.Summary, community.SummaryVersion, normalizeCommunityLogicalID(community), community.MemberCount,
		community.SourceCount, pq.Array(community.TopEntities),
		pq.Array(community.TopPredicates), community.SourceFingerprint, community.SummaryInputHash,
		community.SummaryProviderModel, community.SummaryPromptHash, community.SummaryResponseHash).Error; err != nil {
		return err
	}
	for _, membership := range community.Memberships {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO community_memberships (
				team_id, space_id, space_generation, community_id, entity_id, rank, membership_score, source_count
			) VALUES (
				?::uuid, ?::uuid, ?, ?::uuid, ?::uuid, ?, ?, ?
			)
		`, input.TeamID, fence.ID, fence.Generation, community.CommunityID, membership.EntityID,
			membership.Rank, membership.MembershipScore, membership.SourceCount).Error; err != nil {
			return err
		}
	}
	for _, source := range community.Sources {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO community_sources (
				team_id, space_id, space_generation, community_id, relationship_id, owner_profile_id,
				relationship_version, source_rank, semantic_group_key, source_state_hash
			) VALUES (
				?::uuid, ?::uuid, ?, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?
			)
		`, input.TeamID, fence.ID, fence.Generation, community.CommunityID, source.RelationshipID,
			source.OwnerProfileID, source.RelationshipVersion, source.SourceRank,
			source.SemanticGroupKey, source.SourceStateHash).Error; err != nil {
			return err
		}
	}
	return nil
}
