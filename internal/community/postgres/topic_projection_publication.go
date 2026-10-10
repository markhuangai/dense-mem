package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (r *Store) AppendTopicProjection(ctx context.Context, batch community.TopicProjectionBatch, now time.Time) error {
	work := batch.Work
	if len(work.Inputs) > ontology.TopicProjectionPageSize || len(batch.Sources) > ontology.TopicProjectionPageSize {
		return ontology.ErrInvalid
	}
	return r.withProjectionTx(ctx, func(tx *gorm.DB) error {
		if err := lockTopicProjection(tx, work, now); err != nil {
			return err
		}
		admitted, err := projectionAdmission(tx)
		if err != nil {
			return err
		}
		if !admitted {
			return ontology.ErrMaintenanceDisabled
		}
		fence, err := loadTeamSharedSpaceFence(ctx, tx, work.TeamID)
		if err != nil {
			return err
		}
		if fence.ID != work.SpaceID || fence.Generation != work.Generation {
			return community.ErrCommunitySourceStale
		}
		if err := lockProjectionDependencies(tx, work); err != nil {
			return err
		}
		if err := ensureCommunitySourcesCurrent(ctx, tx, work.TeamID, fence, batch.Sources); err != nil {
			return err
		}
		for key, version := range work.Dependencies {
			if err := tx.Exec(`INSERT INTO community_topic_dependencies(team_id,space_id,space_generation,community_id,dependency_key,version,fingerprint)
				VALUES(?::uuid,?::uuid,?,?::uuid,?,?,?) ON CONFLICT(team_id,community_id,dependency_key) DO UPDATE SET fingerprint=EXCLUDED.fingerprint`,
				work.TeamID, work.SpaceID, work.Generation, work.CommunityID, key, version, work.DependencyFingerprints[key]).Error; err != nil {
				return err
			}
		}
		for _, source := range batch.Sources {
			if err := tx.Exec(`INSERT INTO community_sources(team_id,space_id,space_generation,community_id,relationship_id,
				owner_profile_id,relationship_version,source_rank,semantic_group_key,source_state_hash)
				VALUES(?::uuid,?::uuid,?,?::uuid,?::uuid,?::uuid,?,0,?,?)`, work.TeamID, work.SpaceID, work.Generation,
				work.CommunityID, source.RelationshipID, source.OwnerProfileID, source.RelationshipVersion, source.SemanticGroupKey, source.SourceStateHash).Error; err != nil {
				return err
			}
		}
		for _, member := range batch.Memberships {
			if err := tx.Exec(`INSERT INTO community_memberships(team_id,space_id,space_generation,community_id,entity_id,rank,membership_score,source_count)
				VALUES(?::uuid,?::uuid,?,?::uuid,?::uuid,0,1,?) ON CONFLICT(team_id,community_id,entity_id)
				DO UPDATE SET source_count=community_memberships.source_count+EXCLUDED.source_count`, work.TeamID, work.SpaceID, work.Generation,
				work.CommunityID, member.EntityID, member.SourceCount).Error; err != nil {
				return err
			}
		}
		var previous string
		if err := tx.Raw(`SELECT source_fingerprint FROM community_records WHERE team_id=?::uuid AND community_id=?::uuid`, work.TeamID, work.CommunityID).Row().Scan(&previous); err != nil {
			return err
		}
		fingerprint, err := community.TopicProjectionFingerprint(previous, batch)
		if err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE community_records SET source_fingerprint=?,updated_at=? WHERE team_id=?::uuid AND community_id=?::uuid`,
			fingerprint, now, work.TeamID, work.CommunityID).Error; err != nil {
			return err
		}
		if work.More {
			if err := tx.Exec(`UPDATE community_topic_work SET assignment_cursor=?,relationship_cursor=?,lease_token=NULL,lease_until=NULL
				WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`, work.Cursor.AssignmentID,
				work.Cursor.RelationshipID, work.TeamID, work.SpaceID, work.Generation, work.TopicID).Error; err != nil {
				return err
			}
		} else if err := publishTopicProjection(ctx, tx, work, fingerprint, now); err != nil {
			return err
		}
		return r.topicRelease(ctx, tx, topicMaintenanceTurn(work, now))
	})
}

func publishTopicProjection(ctx context.Context, tx *gorm.DB, work community.TopicProjectionWork, fingerprint string, now time.Time) error {
	var memberCount, sourceCount int
	if err := tx.Raw(`SELECT (SELECT count(*) FROM community_memberships WHERE team_id=?::uuid AND community_id=?::uuid),
		(SELECT count(*) FROM community_sources WHERE team_id=?::uuid AND community_id=?::uuid)`,
		work.TeamID, work.CommunityID, work.TeamID, work.CommunityID).Row().Scan(&memberCount, &sourceCount); err != nil {
		return err
	}
	status, recordStatus := "current", "current"
	if sourceCount == 0 {
		status, recordStatus = "empty", "superseded"
	}
	if !work.Topic.Current {
		status, recordStatus = "retired", "superseded"
	}
	topEntities, topPredicates := []string{}, []string{}
	if sourceCount > 0 && work.Topic.Current {
		if err := rankTopicProjection(ctx, tx, work, &topEntities, &topPredicates); err != nil {
			return err
		}
	}
	summary := ""
	if work.Topic.Definition != nil {
		summary = community.TopicProjectionSummary(*work.Topic.Definition, topEntities, topPredicates)
	}
	var previousID string
	err := tx.Raw(`SELECT community_id::text FROM community_records WHERE team_id=?::uuid AND space_id=?::uuid
		AND space_generation=? AND topic_id=?::uuid AND status='current' AND source_fingerprint=? AND community_id<>?::uuid`,
		work.TeamID, work.SpaceID, work.Generation, work.TopicID, fingerprint, work.CommunityID).Row().Scan(&previousID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previousID != "" && recordStatus == "current" {
		if err := tx.Exec(`INSERT INTO community_topic_dependencies(team_id,space_id,space_generation,community_id,dependency_key,version,fingerprint)
			SELECT team_id,space_id,space_generation,?::uuid,dependency_key,version,fingerprint FROM community_topic_dependencies
			WHERE team_id=?::uuid AND community_id=?::uuid ON CONFLICT(team_id,community_id,dependency_key)
			DO UPDATE SET version=EXCLUDED.version,fingerprint=EXCLUDED.fingerprint`, previousID, work.TeamID, work.CommunityID).Error; err != nil {
			return err
		}
		recordStatus = "superseded"
	} else {
		if err := tx.Exec(`UPDATE community_records SET status='superseded',superseded_at=?,updated_at=?
			WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid AND status='current'`,
			now, now, work.TeamID, work.SpaceID, work.Generation, work.TopicID).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec(`UPDATE community_records SET status=?,summary=?,member_count=?,source_count=?,top_entities=?::text[],
		top_predicates=?::text[],summary_input_hash=?,summary_generated_at=?,updated_at=?
		WHERE team_id=?::uuid AND community_id=?::uuid AND status='building'`, recordStatus, summary, memberCount, sourceCount,
		pq.Array(topEntities), pq.Array(topPredicates), fingerprint, now, now, work.TeamID, work.CommunityID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE community_snapshot_runs SET status='completed',source_fingerprint=?,node_count=?,edge_count=0,
		community_count=?,error='',completed_at=?,updated_at=? WHERE team_id=?::uuid AND run_id=?::uuid`,
		fingerprint, sourceCount, boolInt(recordStatus == "current"), now, now, work.TeamID, work.RunID).Error; err != nil {
		return err
	}
	if previousID != "" && status == "current" {
		work.CommunityID = previousID
	}
	return tx.Exec(`UPDATE community_topic_work SET status=?,community_id=?::uuid,assignment_cursor='',relationship_cursor='',
		lease_token=NULL,lease_until=NULL,failure_code='' WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`,
		status, work.CommunityID, work.TeamID, work.SpaceID, work.Generation, work.TopicID).Error
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rankTopicProjection(ctx context.Context, tx *gorm.DB, work community.TopicProjectionWork, entities, predicates *[]string) error {
	if err := tx.WithContext(ctx).Exec(`WITH ranked AS (SELECT relationship_id,row_number() OVER(ORDER BY relationship_id)-1 AS rank
		FROM community_sources WHERE team_id=?::uuid AND community_id=?::uuid)
		UPDATE community_sources AS source SET source_rank=ranked.rank FROM ranked
		WHERE source.team_id=?::uuid AND source.community_id=?::uuid AND source.relationship_id=ranked.relationship_id`,
		work.TeamID, work.CommunityID, work.TeamID, work.CommunityID).Error; err != nil {
		return err
	}
	if err := tx.Exec(`WITH ranked AS (SELECT membership.entity_id,row_number() OVER(ORDER BY membership.source_count DESC,
		COALESCE(name.display_name,membership.entity_id::text),membership.entity_id)-1 AS rank
		FROM community_memberships AS membership LEFT JOIN LATERAL (SELECT display_name FROM entity_names AS name
		WHERE name.team_id=membership.team_id AND name.space_id=membership.space_id AND name.space_generation=membership.space_generation
		AND name.entity_id=membership.entity_id AND name.name_kind='canonical' AND name.valid_to IS NULL
		ORDER BY name.created_at DESC,name.entity_name_id DESC LIMIT 1) AS name ON TRUE
		WHERE membership.team_id=?::uuid AND membership.community_id=?::uuid)
		UPDATE community_memberships AS membership SET rank=ranked.rank FROM ranked
		WHERE membership.team_id=?::uuid AND membership.community_id=?::uuid AND membership.entity_id=ranked.entity_id`,
		work.TeamID, work.CommunityID, work.TeamID, work.CommunityID).Error; err != nil {
		return err
	}
	if err := tx.Raw(`SELECT COALESCE(name.display_name,membership.entity_id::text) FROM community_memberships AS membership
		LEFT JOIN LATERAL (SELECT display_name FROM entity_names AS name WHERE name.team_id=membership.team_id
		AND name.space_id=membership.space_id AND name.space_generation=membership.space_generation AND name.entity_id=membership.entity_id
		AND name.name_kind='canonical' AND name.valid_to IS NULL ORDER BY name.created_at DESC,name.entity_name_id DESC LIMIT 1) AS name ON TRUE
		WHERE membership.team_id=?::uuid AND membership.community_id=?::uuid ORDER BY membership.rank LIMIT ?`,
		work.TeamID, work.CommunityID, community.MaxRecallCommunityTopEntities).Scan(entities).Error; err != nil {
		return err
	}
	return tx.Raw(`SELECT relationship.predicate_key FROM community_sources AS source JOIN relationship_records AS relationship
		ON relationship.team_id=source.team_id AND relationship.relationship_id=source.relationship_id
		AND relationship.version=source.relationship_version AND relationship.space_id=source.space_id AND relationship.space_generation=source.space_generation
		WHERE source.team_id=?::uuid AND source.community_id=?::uuid GROUP BY relationship.predicate_key
		ORDER BY count(*) DESC,relationship.predicate_key LIMIT 5`, work.TeamID, work.CommunityID).Scan(predicates).Error
}
