package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

const currentCommunityParent = `(result.status <> 'placed' OR EXISTS(
	SELECT 1 FROM topic_record_heads AS parent
	WHERE parent.team_id=result.team_id AND parent.shared_space_id=result.shared_space_id
	AND parent.space_generation=result.space_generation AND parent.record_id=result.parent_topic_id
	AND parent.version=result.parent_topic_version AND parent.kind='topic' AND NOT parent.retired
))`

const communityWorkReady = `work.ready_at<=statement_timestamp() AND NOT EXISTS(
	SELECT 1 FROM topic_maintenance_batches AS batch WHERE batch.team_id=work.team_id
	AND batch.shared_space_id=work.shared_space_id AND batch.space_generation=work.space_generation
	AND batch.community_topic_id=work.topic_id AND batch.status='running' AND batch.lease_until>statement_timestamp()
)`

func (s *Store) ClaimCommunity(ctx context.Context, turn topic.MaintenanceTurn, windowID string, now time.Time) (*topic.CommunityInput, *topic.MaintenanceClaim, error) {
	var input *topic.CommunityInput
	err := s.withScope(ctx, turn.TeamID, true, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return topic.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		var body []byte
		var workID, priorParent string
		err := tx.Raw(`WITH candidate AS MATERIALIZED (
			SELECT work.* FROM topic_maintenance_community_work AS work
			WHERE work.team_id=?::uuid AND work.shared_space_id=?::uuid AND work.space_generation=?
			AND `+communityWorkReady+` ORDER BY work.ready_at,work.topic_id LIMIT 1
		)
		SELECT revision.body,candidate.work_id::text,COALESCE(candidate.parent_topic_id::text,'')
		FROM candidate
		LEFT JOIN topic_record_heads AS head ON
			(head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version)=
			(candidate.team_id,candidate.shared_space_id,candidate.space_generation,candidate.topic_id,candidate.topic_version)
			AND head.kind='topic' AND NOT head.retired
		LEFT JOIN topic_revision_records AS revision
			ON (revision.team_id,revision.shared_space_id,revision.space_generation,revision.record_id,revision.version)=
			   (head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version)`,
			fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&body, &workID, &priorParent)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return err
		}
		input = &topic.CommunityInput{Topic: communityCandidate(record), Current: record.Definition.ParentID, WorkID: workID}
		if input.Current == "" {
			input.Current = priorParent
		}
		text := record.Definition.Label + " " + record.Definition.Description
		words := []topic.SourceSnapshot{{State: map[string]string{"content": text}}}
		var forced []string
		if input.Current != "" {
			forced = []string{input.Current}
		}
		candidates, err := s.organizationVocabulary(tx, fence, words, forced)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if candidate.ID != record.ID && candidate.Definition != nil {
				input.Candidates = append(input.Candidates, communityCandidate(candidate.Record))
			}
		}
		return nil
	})
	if err != nil || input == nil {
		return nil, nil, err
	}
	claim := &topic.MaintenanceClaim{ID: uuid.NewString(), WindowID: windowID, TeamID: turn.TeamID,
		SpaceID: turn.SpaceID, Generation: turn.Generation, LeaseToken: turn.LeaseToken, LeaseUntil: turn.LeaseUntil,
		Sources: []topic.SourceHandle{}, Revisions: map[string]int64{input.Topic.ID: input.Topic.Version}}
	claim, err = s.admitMaintenanceBatch(ctx, turn, claim, input, now)
	if err != nil || claim == nil {
		return nil, nil, err
	}
	return input, claim, nil
}

func communityCandidate(record topic.Record) topic.CommunityCandidate {
	return topic.CommunityCandidate{ID: record.ID, Version: record.Version, Name: record.Definition.Label, Description: record.Definition.Description}
}

func (s *Store) CompleteCommunity(ctx context.Context, claim topic.MaintenanceClaim, input topic.CommunityInput, outcome topic.CommunityOutcome, failure string, now time.Time) error {
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		fence, err := sharedScope(ctx, tx, claim.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != claim.SpaceID || fence.Generation != claim.Generation {
			return topic.ErrLeaseLost
		}
		if claim.Revisions[input.Topic.ID] != input.Topic.Version {
			return topic.ErrInvalid
		}
		if err := maintenancePublicationFence(topic.WithMaintenanceClaim(ctx, claim), tx, fence); err != nil {
			return err
		}
		// Serialize hierarchy changes with catalog publication and rollback.
		var catalogRevision int64
		if err := tx.Raw(`SELECT revision FROM topic_catalog_heads
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? FOR UPDATE`,
			fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&catalogRevision); err != nil {
			return err
		}
		ids := []string{input.Topic.ID}
		versions := map[string]int64{input.Topic.ID: input.Topic.Version}
		var parentVersion any
		if outcome.ParentID != "" {
			for _, candidate := range input.Candidates {
				if candidate.ID == outcome.ParentID {
					versions[candidate.ID] = candidate.Version
				}
			}
			if versions[outcome.ParentID] == 0 || outcome.ParentID == input.Topic.ID {
				return topic.ErrInvalid
			}
			parentVersion = versions[outcome.ParentID]
			ids = append(ids, outcome.ParentID)
		}
		rows, err := tx.Raw(`SELECT record_id::text,version FROM topic_record_heads
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND kind='topic' AND NOT retired
			AND record_id=ANY(?::uuid[]) ORDER BY record_id FOR UPDATE`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var version int64
			if err := rows.Scan(&id, &version); err != nil {
				rows.Close()
				return err
			}
			if versions[id] != version {
				rows.Close()
				return topic.ErrSourceStale
			}
			delete(versions, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(versions) != 0 {
			return topic.ErrSourceStale
		}
		if outcome.ParentID != "" {
			edge := communityEdge{topicID: input.Topic.ID, parentID: outcome.ParentID}
			conflict, err := conflictingCommunityEdge(tx, fence, []communityEdge{edge})
			if err != nil {
				return err
			}
			if conflict != nil {
				return topic.ErrConflict
			}
		}
		settled := outcome.Status == "placed" || outcome.Status == "standalone"
		updated := tx.Exec(`UPDATE topic_maintenance_community_work SET
			ready_at=CASE WHEN ? THEN NULL ELSE statement_timestamp()+interval '1 day' END,
			parent_topic_id=CASE WHEN ? THEN NULLIF(?,'')::uuid ELSE parent_topic_id END,
			parent_topic_version=CASE WHEN ? THEN ?::bigint ELSE parent_topic_version END
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
			AND topic_id=?::uuid AND topic_version=? AND work_id=?::uuid`, settled, settled, outcome.ParentID,
			settled, parentVersion, fence.TeamID, fence.SpaceID, fence.Generation, input.Topic.ID, input.Topic.Version, input.WorkID)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return topic.ErrSourceStale
		}
		if err := tx.Exec(`INSERT INTO community_placement_outcomes(team_id,shared_space_id,space_generation,topic_id,topic_version,input_hash,method,status,parent_topic_id,parent_topic_version,operation_id)
			VALUES(?::uuid,?::uuid,?,?::uuid,?,?,?,?,NULLIF(?,'')::uuid,?::bigint,?::uuid)`, fence.TeamID, fence.SpaceID, fence.Generation,
			input.Topic.ID, input.Topic.Version, outcome.InputHash, outcome.Method, outcome.Status, outcome.ParentID, parentVersion, claim.ID).Error; err != nil {
			return err
		}
		phase := "batch_fence"
		if err := completeMaintenanceBatch(tx, claim, topic.OrganizationResult{}, failure, now, &phase); err != nil {
			return err
		}
		if failure == "" {
			return tx.Exec(`UPDATE topic_maintenance_state SET last_successful_progress=clock_timestamp() WHERE singleton`).Error
		}
		return nil
	})
}

type communityEdge struct{ topicID, parentID string }

func conflictingCommunityEdge(tx *gorm.DB, fence scope, edges []communityEdge) (*communityEdge, error) {
	if len(edges) == 0 {
		return nil, nil
	}
	topics := make([]string, 0, len(edges))
	parents := make([]string, 0, len(edges))
	for _, edge := range edges {
		topics = append(topics, edge.topicID)
		parents = append(parents, edge.parentID)
	}
	var ordinal int
	err := tx.Raw(`WITH RECURSIVE chain(topic_id,id,ordinal,depth) AS (
		SELECT edge.topic_id,edge.parent_id,edge.ordinal,1
		FROM unnest(?::uuid[],?::uuid[]) WITH ORDINALITY AS edge(topic_id,parent_id,ordinal)
		UNION ALL
		SELECT chain.topic_id,CASE WHEN placement.status IS NULL THEN NULLIF(payload.body->'definition'->>'parent_id','')::uuid
			ELSE placement.parent_topic_id END,chain.ordinal,chain.depth+1
		FROM chain JOIN topic_record_heads AS head ON head.record_id=chain.id
		JOIN topic_revision_payloads AS payload USING(team_id,shared_space_id,space_generation,record_id,version)
		LEFT JOIN LATERAL (SELECT status,parent_topic_id FROM community_placement_outcomes AS result
			WHERE result.team_id=head.team_id AND result.shared_space_id=head.shared_space_id AND result.space_generation=head.space_generation
			AND result.topic_id=head.record_id AND result.topic_version=head.version AND result.status IN ('placed','standalone')
			AND `+currentCommunityParent+` ORDER BY result.created_at DESC,result.outcome_id LIMIT 1) AS placement ON true
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=? AND head.kind='topic' AND NOT head.retired
		AND chain.depth<=?
	) SELECT COALESCE(min(ordinal),0) FROM chain WHERE id=topic_id OR (id IS NOT NULL AND depth>?)`,
		pq.Array(topics), pq.Array(parents), fence.TeamID, fence.SpaceID, fence.Generation,
		topic.MaxParentDepth, topic.MaxParentDepth).Row().Scan(&ordinal)
	if err != nil || ordinal == 0 {
		return nil, err
	}
	return &edges[ordinal-1], nil
}

func attachCommunityPlacement(tx *gorm.DB, fence scope, views []topic.RecordView) error {
	var ids []string
	positions := make(map[string]int, len(views))
	for i, view := range views {
		if view.Kind == topic.Topic && view.Current && !view.Retired {
			ids = append(ids, view.ID)
			positions[view.ID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Raw(`SELECT DISTINCT ON (result.topic_id) result.topic_id::text,result.status,COALESCE(result.parent_topic_id::text,'')
		FROM community_placement_outcomes AS result
		JOIN topic_record_heads AS head ON head.team_id=result.team_id AND head.shared_space_id=result.shared_space_id
		 AND head.space_generation=result.space_generation AND head.record_id=result.topic_id AND head.version=result.topic_version
		WHERE result.team_id=?::uuid AND result.shared_space_id=?::uuid AND result.space_generation=?
		 AND result.topic_id=ANY(?::uuid[]) AND `+currentCommunityParent+`
		ORDER BY result.topic_id,result.created_at DESC,result.outcome_id`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, parent string
		if err := rows.Scan(&id, &status, &parent); err != nil {
			return err
		}
		index := positions[id]
		views[index].CommunityParentID, views[index].CommunityPlacement = parent, status
	}
	return rows.Err()
}
