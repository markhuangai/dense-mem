package postgres

import (
	"context"

	community "github.com/markhuangai/dense-mem/internal/community/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"gorm.io/gorm"
)

func authorizeTopicProjectionTeam(ctx context.Context, teamID string) error {
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID.String() != teamID {
		return ontology.ErrUnauthorized
	}
	return nil
}

func (r *Store) TopicProjectionCoverage(ctx context.Context, teamID string) (community.TopicProjectionCoverage, error) {
	result := community.TopicProjectionCoverage{}
	if err := authorizeTopicProjectionTeam(ctx, teamID); err != nil {
		return result, err
	}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		var err error
		result, err = loadTopicProjectionCoverage(ctx, tx, teamID)
		return err
	})
	return result, err
}

const topicProjectionFromSQL = `
 FROM ontology_record_heads AS head LEFT JOIN community_topic_work AS work
 ON work.team_id=head.team_id AND work.space_id=head.shared_space_id AND work.space_generation=head.space_generation
 AND work.topic_id=head.record_id CROSS JOIN scope`

const topicProjectionWhereSQL = `
 WHERE head.team_id=scope.team_id AND head.shared_space_id=scope.space_id AND head.space_generation=scope.generation
 AND head.kind='topic' AND NOT head.retired`

const topicProjectionCountsSQL = `
 SELECT count(*)::int AS total_topics,count(*) FILTER(WHERE status='current' AND fresh)::int AS current_topics,
 count(*) FILTER(WHERE status IS NULL OR status IN ('pending','building'))::int AS pending_topics,
 count(*) FILTER(WHERE status IN ('current','empty') AND NOT fresh OR status='retired')::int AS stale_topics,
 count(*) FILTER(WHERE status='failed')::int AS failed_topics FROM topics`

const topicProjectionCoverageResultSQL = `
 SELECT counts.*, (EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE team_id=scope.team_id
 AND shared_space_id=scope.space_id AND space_generation=scope.generation AND discovery_kind=4)
 AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_sources WHERE team_id=scope.team_id AND shared_space_id=scope.space_id
 AND space_generation=scope.generation AND eligible AND status NOT IN ('organized','unavailable'))
 AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_markers WHERE team_id=scope.team_id AND shared_space_id=scope.space_id AND space_generation=scope.generation)
 AND counts.pending_topics+counts.stale_topics+counts.failed_topics=0) AS coverage_complete
 FROM counts CROSS JOIN scope`

func loadTopicProjectionCoverage(ctx context.Context, tx *gorm.DB, teamID string) (community.TopicProjectionCoverage, error) {
	result := community.TopicProjectionCoverage{}
	fence, err := loadTeamSharedSpaceFence(ctx, tx, teamID)
	if err != nil {
		return result, err
	}
	err = tx.Raw(`WITH scope AS (SELECT ?::uuid AS team_id,?::uuid AS space_id,?::bigint AS generation),
 topics AS MATERIALIZED (SELECT work.status,
 CASE WHEN work.community_id IS NULL THEN false ELSE dense_mem_community_topic_current(head.team_id,work.community_id) END AS fresh`+
		topicProjectionFromSQL+topicProjectionWhereSQL+`), counts AS (`+topicProjectionCountsSQL+`)`+
		topicProjectionCoverageResultSQL, teamID, fence.ID, fence.Generation).Row().Scan(
		&result.TotalTopics, &result.CurrentTopics, &result.PendingTopics, &result.StaleTopics, &result.FailedTopics, &result.CoverageComplete)
	return result, err
}
