package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (r *Store) discoverTopicProjections(ctx context.Context, tx *gorm.DB) error {
	var cursor ontology.TopicCatalogCursor
	if err := tx.Raw(`SELECT after_team,after_topic FROM community_topic_discovery WHERE singleton FOR UPDATE`).Row().Scan(&cursor.TeamID, &cursor.TopicID); err != nil {
		return err
	}
	page, err := r.topicCatalog(ctx, tx, cursor)
	if err != nil {
		return err
	}
	for _, topic := range page.Topics {
		if err := tx.Exec(`INSERT INTO community_topic_work(team_id,space_id,space_generation,topic_id,topic_version)
			VALUES(?::uuid,?::uuid,?,?::uuid,?) ON CONFLICT(team_id,space_id,space_generation,topic_id)
			DO UPDATE SET topic_version=EXCLUDED.topic_version,
			status=CASE WHEN community_topic_work.status='building' THEN 'building' ELSE 'pending' END,failure_code=''
			WHERE community_topic_work.topic_version<>EXCLUDED.topic_version`, topic.TeamID, topic.SpaceID, topic.Generation, topic.TopicID, topic.Version).Error; err != nil {
			return err
		}
	}
	if !page.More {
		page.Next = ontology.TopicCatalogCursor{}
	}
	return tx.Exec(`UPDATE community_topic_discovery SET after_team=?,after_topic=? WHERE singleton`, page.Next.TeamID, page.Next.TopicID).Error
}

func (r *Store) ClaimTopicProjection(ctx context.Context, now time.Time) (*community.TopicProjectionWork, error) {
	var work *community.TopicProjectionWork
	err := r.withProjectionTx(ctx, func(tx *gorm.DB) error {
		admitted, err := projectionAdmission(tx)
		if err != nil || !admitted {
			return err
		}
		if err := r.discoverTopicProjections(ctx, tx); err != nil {
			return err
		}
		candidate := &community.TopicProjectionWork{}
		var status string
		err = tx.Raw(`SELECT work.team_id::text,work.space_id::text,work.space_generation,work.topic_id::text,
			COALESCE(work.community_id::text,''),COALESCE(work.run_id::text,''),work.assignment_cursor,work.relationship_cursor,work.status
			FROM community_topic_work AS work JOIN memory_spaces AS space
			ON space.team_id=work.team_id AND space.id=work.space_id AND space.generation=work.space_generation
			AND space.lifecycle_state='active' JOIN teams AS team ON team.id=work.team_id AND team.status='active' AND team.deleted_at IS NULL
			WHERE (work.lease_until IS NULL OR work.lease_until<=?) AND (work.status IN ('pending','building')
			OR (work.community_id IS NOT NULL AND NOT dense_mem_community_topic_current(work.team_id,work.community_id)))
			ORDER BY work.last_turn,work.team_id,work.topic_id LIMIT 1 FOR UPDATE OF work SKIP LOCKED`, now).Row().Scan(
			&candidate.TeamID, &candidate.SpaceID, &candidate.Generation, &candidate.TopicID, &candidate.CommunityID, &candidate.RunID,
			&candidate.Cursor.AssignmentID, &candidate.Cursor.RelationshipID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var current bool
		if candidate.CommunityID != "" {
			if err := tx.Raw(`SELECT dense_mem_community_topic_current(?::uuid,?::uuid)`, candidate.TeamID, candidate.CommunityID).Row().Scan(&current); err != nil {
				return err
			}
		}
		candidate.LeaseToken = uuid.NewString()
		admitted, err = r.topicAdmission(ctx, tx, topicMaintenanceTurn(*candidate, now), now)
		if err != nil {
			return err
		}
		if !admitted {
			return tx.Exec(`UPDATE community_topic_work SET last_turn=?
				WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`,
				now, candidate.TeamID, candidate.SpaceID, candidate.Generation, candidate.TopicID).Error
		}
		if status != "building" || !current {
			if err := startTopicProjection(tx, candidate, now); err != nil {
				return err
			}
		}
		if err := tx.Exec(`UPDATE community_topic_work SET status='building',lease_token=?::uuid,lease_until=?,last_turn=?,failure_code=''
			WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`, candidate.LeaseToken,
			now.Add(15*time.Minute), now, candidate.TeamID, candidate.SpaceID, candidate.Generation, candidate.TopicID).Error; err != nil {
			return err
		}
		work = candidate
		return nil
	})
	if err != nil || work == nil {
		return work, err
	}
	err = r.db.WithContext(ctx).Transaction(func(snapshot *gorm.DB) error {
		return r.rls.WithSystemTx(ctx, snapshot, func(tx *gorm.DB) error {
			page, err := r.topicMembership(ctx, tx, work.TeamID, work.TopicID, work.Cursor)
			if err != nil {
				return err
			}
			work.Topic, work.Cursor, work.More = page.Topic, page.Next, page.More
			if page.Topic.Current && len(page.RelationshipIDs) > 0 {
				work.Inputs, err = listCommunityInputsTx(ctx, tx, semanticSpaceFence{ID: work.SpaceID, Generation: work.Generation}, CommunityInputListInput{
					TeamID: work.TeamID, Limit: ontology.TopicProjectionPageSize, RelationshipIDs: page.RelationshipIDs})
				if err != nil {
					return err
				}
				var existing []string
				if err := tx.Raw(`SELECT relationship_id::text FROM community_sources WHERE team_id=?::uuid AND community_id=?::uuid
				AND relationship_id=ANY(?::uuid[])`, work.TeamID, work.CommunityID, pq.Array(page.RelationshipIDs)).Scan(&existing).Error; err != nil {
					return err
				}
				seen := map[string]bool{}
				for _, id := range existing {
					seen[id] = true
				}
				inputs := work.Inputs[:0]
				for _, input := range work.Inputs {
					if !seen[input.RelationshipID] {
						inputs = append(inputs, input)
					}
				}
				work.Inputs = inputs
			}
			keys, err := projectionSourceKeys(tx, *work, page)
			if err != nil {
				return err
			}
			work.Dependencies, err = captureProjectionVersions(tx, *work, keys)
			if err != nil {
				return err
			}
			work.DependencyFingerprints = map[string]string{"definition:" + page.Topic.ID: page.Topic.Fingerprint}
			for _, record := range page.Records {
				work.Fingerprints = append(work.Fingerprints, record.ID+":"+record.Fingerprint)
				work.DependencyFingerprints["definition:"+record.ID] = record.Fingerprint
				for _, source := range record.Sources {
					work.DependencyFingerprints[ontology.SourceKey(source.SourceHandle)] = source.Fingerprint
				}
			}
			work.Fingerprints = append(work.Fingerprints, page.Topic.ID+":"+page.Topic.Fingerprint)
			return nil
		})
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "40001" {
		err = errors.Join(community.ErrCommunitySourceStale, err)
	}
	return work, err
}

func startTopicProjection(tx *gorm.DB, work *community.TopicProjectionWork, now time.Time) error {
	if work.CommunityID != "" {
		if err := tx.Exec(`UPDATE community_records SET status='superseded',superseded_at=?,updated_at=?
			WHERE team_id=?::uuid AND community_id=?::uuid AND status='building'`, now, now, work.TeamID, work.CommunityID).Error; err != nil {
			return err
		}
	}
	work.CommunityID, work.RunID = uuid.NewString(), uuid.NewString()
	work.Cursor = ontology.TopicMembershipCursor{}
	if err := tx.Exec(`INSERT INTO community_snapshot_runs(team_id,space_id,space_generation,run_id,window_key,status,
		algorithm_kind,algorithm_version,profile_version,max_nodes,max_edges,started_at)
		VALUES(?::uuid,?::uuid,?,?::uuid,?,'running','ontology_topic','v1','postgres-v2',0,0,?)`,
		work.TeamID, work.SpaceID, work.Generation, work.RunID, "topic:"+work.TopicID+":"+work.CommunityID, now).Error; err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO community_records(team_id,space_id,space_generation,community_id,run_id,ordinal,status,
		logical_community_id,topic_id,summary_version) VALUES(?::uuid,?::uuid,?,?::uuid,?::uuid,0,'building',?::uuid,?::uuid,?)`,
		work.TeamID, work.SpaceID, work.Generation, work.CommunityID, work.RunID, work.TopicID, work.TopicID, community.TopicProjectionSummaryVersion).Error; err != nil {
		return err
	}
	keys := []string{"topic:" + work.TopicID, "definition:" + work.TopicID}
	versions, err := captureProjectionVersions(tx, *work, keys)
	if err != nil {
		return err
	}
	for key, version := range versions {
		if err := tx.Exec(`INSERT INTO community_topic_dependencies(team_id,space_id,space_generation,community_id,dependency_key,version)
			VALUES(?::uuid,?::uuid,?,?::uuid,?,?)`, work.TeamID, work.SpaceID, work.Generation, work.CommunityID, key, version).Error; err != nil {
			return err
		}
	}
	return tx.Exec(`UPDATE community_topic_work SET community_id=?::uuid,run_id=?::uuid,assignment_cursor='',relationship_cursor=''
		WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`,
		work.CommunityID, work.RunID, work.TeamID, work.SpaceID, work.Generation, work.TopicID).Error
}

func lockTopicProjection(tx *gorm.DB, work community.TopicProjectionWork, now time.Time) error {
	var token string
	err := tx.Raw(`SELECT COALESCE(lease_token::text,'') FROM community_topic_work
		WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid
		AND community_id=?::uuid AND status='building' AND lease_until>? FOR UPDATE`,
		work.TeamID, work.SpaceID, work.Generation, work.TopicID, work.CommunityID, now).Row().Scan(&token)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && token != work.LeaseToken) {
		return community.ErrCommunityRunAlreadyClaimed
	}
	return err
}

func (r *Store) FailTopicProjection(ctx context.Context, work community.TopicProjectionWork, code string, now time.Time) error {
	return r.withProjectionTx(ctx, func(tx *gorm.DB) error {
		if err := lockTopicProjection(tx, work, now); err != nil {
			return err
		}
		status := "failed"
		if code == "source_changed" {
			status = "pending"
		}
		if code == "interrupted" {
			status = "building"
		}
		if status == "failed" {
			if err := tx.Exec(`UPDATE community_topic_dependencies AS dependency SET version=live.version
				FROM community_topic_versions AS live WHERE dependency.team_id=?::uuid AND dependency.community_id=?::uuid
				AND live.team_id=dependency.team_id AND live.space_id=dependency.space_id AND live.space_generation=dependency.space_generation
				AND live.dependency_key=dependency.dependency_key`, work.TeamID, work.CommunityID).Error; err != nil {
				return err
			}
		}
		if status != "building" {
			if err := tx.Exec(`UPDATE community_snapshot_runs SET status='failed',error=?,completed_at=?,updated_at=?
				WHERE team_id=?::uuid AND run_id=?::uuid AND status='running'`, code, now, now, work.TeamID, work.RunID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`UPDATE community_topic_work SET status=?,failure_code=?,lease_token=NULL,lease_until=NULL
			WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND topic_id=?::uuid`, status, code,
			work.TeamID, work.SpaceID, work.Generation, work.TopicID).Error; err != nil {
			return err
		}
		return r.topicRelease(ctx, tx, topicMaintenanceTurn(work, now))
	})
}

var _ community.TopicProjectionRepository = (*Store)(nil)
