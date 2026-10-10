package postgres

import (
	"context"

	community "github.com/markhuangai/dense-mem/internal/community/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"gorm.io/gorm"
)

func (r *Store) TopicProjectionCoverage(ctx context.Context, teamID string) (community.TopicProjectionCoverage, error) {
	result := community.TopicProjectionCoverage{}
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID.String() != teamID {
		return result, ontology.ErrUnauthorized
	}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, teamID)
		if err != nil {
			return err
		}
		var upstreamComplete bool
		if err := tx.Raw(`WITH topics AS (
			SELECT head.record_id,work.status,work.community_id,
			CASE WHEN work.community_id IS NULL THEN false ELSE dense_mem_community_topic_current(head.team_id,work.community_id) END AS fresh
			FROM ontology_record_heads AS head LEFT JOIN community_topic_work AS work
			ON work.team_id=head.team_id AND work.space_id=head.shared_space_id AND work.space_generation=head.space_generation
			AND work.topic_id=head.record_id WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
			AND head.kind='topic' AND NOT head.retired
		) SELECT count(*)::int,count(*) FILTER(WHERE status='current' AND fresh)::int,
		count(*) FILTER(WHERE status IS NULL OR status IN ('pending','building'))::int,
		count(*) FILTER(WHERE status IN ('current','empty') AND NOT fresh OR status='retired')::int,
		count(*) FILTER(WHERE status='failed')::int FROM topics`, teamID, fence.ID, fence.Generation).Row().Scan(
			&result.TotalTopics, &result.CurrentTopics, &result.PendingTopics, &result.StaleTopics, &result.FailedTopics); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE team_id=?::uuid
			AND shared_space_id=?::uuid AND space_generation=? AND discovery_kind=4)
			AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid
			AND space_generation=? AND eligible AND status NOT IN ('organized','unavailable'))
			AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?)`,
			teamID, fence.ID, fence.Generation, teamID, fence.ID, fence.Generation, teamID, fence.ID, fence.Generation).Row().Scan(&upstreamComplete); err != nil {
			return err
		}
		result.CoverageComplete = upstreamComplete && result.PendingTopics+result.StaleTopics+result.FailedTopics == 0
		return nil
	})
	return result, err
}
