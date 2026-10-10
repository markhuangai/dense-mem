package postgres

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/lib/pq"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"gorm.io/gorm"
)

func (r *Store) withProjectionTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if _, ok := requestctx.ActorFromContext(ctx); ok {
		return ontology.ErrUnauthorized
	}
	if r == nil || r.db == nil || r.rls == nil || r.topicCatalog == nil || r.topicMembership == nil || r.topicAdmission == nil || r.topicRelease == nil {
		return errors.New("community: topic projection dependencies are required")
	}
	return r.rls.WithSystemTx(ctx, r.db, fn)
}

func projectionAdmission(tx *gorm.DB) (bool, error) {
	var values []struct{ Key, Value string }
	if err := tx.Raw(`SELECT key,value FROM app_config WHERE key=ANY(?::text[]) ORDER BY key FOR SHARE`,
		pq.Array([]string{domain.AppConfigCommunityDetectionEnabled, domain.AppConfigOntologyEnabled})).Scan(&values).Error; err != nil {
		return false, err
	}
	enabled := map[string]bool{}
	for _, item := range values {
		enabled[item.Key] = item.Value == "true"
	}
	if !enabled[domain.AppConfigCommunityDetectionEnabled] || !enabled[domain.AppConfigOntologyEnabled] {
		return false, nil
	}
	var paused bool
	if err := tx.Raw(`SELECT paused FROM ontology_maintenance_state WHERE singleton FOR UPDATE`).Row().Scan(&paused); err != nil {
		return false, err
	}
	return !paused, nil
}

func topicMaintenanceTurn(work community.TopicProjectionWork, now time.Time) ontology.MaintenanceTurn {
	return ontology.MaintenanceTurn{TeamID: work.TeamID, SpaceID: work.SpaceID, Generation: work.Generation, LeaseToken: work.LeaseToken, LeaseUntil: now.Add(15 * time.Minute)}
}

func captureProjectionVersions(tx *gorm.DB, work community.TopicProjectionWork, keys []string) (map[string]int64, error) {
	sort.Strings(keys)
	keys = compactProjectionKeys(keys)
	if err := tx.Exec(`INSERT INTO community_topic_versions(team_id,space_id,space_generation,dependency_key,version)
		SELECT ?::uuid,?::uuid,?,unnest(?::text[]),0 ON CONFLICT DO NOTHING`, work.TeamID, work.SpaceID, work.Generation, pq.Array(keys)).Error; err != nil {
		return nil, err
	}
	rows, err := tx.Raw(`SELECT dependency_key,version FROM community_topic_versions
		WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND dependency_key=ANY(?::text[])
		ORDER BY dependency_key`, work.TeamID, work.SpaceID, work.Generation, pq.Array(keys)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var key string
		var version int64
		if err := rows.Scan(&key, &version); err != nil {
			return nil, err
		}
		result[key] = version
	}
	return result, rows.Err()
}

func compactProjectionKeys(keys []string) []string {
	out := keys[:0]
	for _, key := range keys {
		if len(out) == 0 || out[len(out)-1] != key {
			out = append(out, key)
		}
	}
	return out
}

func projectionSourceKeys(tx *gorm.DB, work community.TopicProjectionWork, page ontology.TopicMembershipPage) ([]string, error) {
	keys := []string{"topic:" + work.TopicID, "definition:" + work.TopicID}
	for _, record := range append([]ontology.Record{page.Topic.Record}, page.Records...) {
		keys = append(keys, "definition:"+record.ID)
		for _, ref := range record.Dependencies {
			keys = append(keys, "definition:"+ref.ID)
		}
		for _, source := range record.Sources {
			keys = append(keys, ontology.SourceKey(source.SourceHandle))
		}
	}
	for _, source := range page.Sources {
		keys = append(keys, ontology.SourceKey(source), "classification:"+ontology.SourceKey(source))
	}
	evidence := []string{}
	relationships := []string{}
	for _, input := range work.Inputs {
		keys = append(keys, "relationship:"+input.RelationshipID, "entity:"+input.SubjectEntityID, "predicate:"+input.PredicateKey)
		if input.ObjectEntityID != "" {
			keys = append(keys, "entity:"+input.ObjectEntityID)
		}
		if input.ObjectValueID != "" {
			keys = append(keys, "value:"+input.ObjectValueID)
		}
		evidence = append(evidence, input.EvidenceIDs...)
	}
	for _, source := range page.Sources {
		switch source.Kind {
		case ontology.EvidenceSource:
			evidence = append(evidence, source.ID)
		case ontology.RelationshipSource:
			relationships = append(relationships, source.ID)
		case ontology.PredicateSource:
			predicateKeys, err := projectionPredicateSourceKeys(tx, work, source)
			if err != nil {
				return nil, err
			}
			keys = append(keys, predicateKeys...)
		}
	}
	declaredKeys, err := projectionRelationshipSourceKeys(tx, work, relationships)
	if err != nil {
		return nil, err
	}
	keys = append(keys, declaredKeys...)
	if len(evidence) == 0 {
		return keys, nil
	}
	rows, err := tx.Raw(`SELECT fragment_id::text,ingest_id::text,COALESCE(source_id::text,'') FROM evidence_fragments
		WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND fragment_id=ANY(?::uuid[])`,
		work.TeamID, work.SpaceID, work.Generation, pq.Array(evidence)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ingest, source string
		if err := rows.Scan(&id, &ingest, &source); err != nil {
			return nil, err
		}
		keys = append(keys, "evidence:"+id, "ingest:"+ingest)
		if source != "" {
			keys = append(keys, "source:"+source)
		}
	}
	return keys, rows.Err()
}

func projectionPredicateSourceKeys(tx *gorm.DB, work community.TopicProjectionWork, source ontology.SourceHandle) ([]string, error) {
	keys := []string{"predicate-membership:" + source.ID + "@" + strconv.FormatInt(source.Version, 10)}
	after := ""
	for {
		var ids []string
		if err := tx.Raw(`SELECT relationship_id::text FROM relationship_records
			WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND predicate_key=? AND predicate_version=?
			AND relationship_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
			ORDER BY relationship_id LIMIT ?`, work.TeamID, work.SpaceID, work.Generation,
			source.ID, source.Version, after, ontology.TopicProjectionPageSize).Scan(&ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			keys = append(keys, "relationship:"+id)
			after = id
		}
		dependencies, err := projectionRelationshipSourceKeys(tx, work, ids)
		if err != nil {
			return nil, err
		}
		keys = append(keys, dependencies...)
		if len(ids) < ontology.TopicProjectionPageSize {
			return keys, nil
		}
	}
}

func projectionRelationshipSourceKeys(tx *gorm.DB, work community.TopicProjectionWork, ids []string) ([]string, error) {
	keys := []string{}
	for offset := 0; offset < len(ids); offset += ontology.TopicProjectionPageSize {
		batch := ids[offset:min(offset+ontology.TopicProjectionPageSize, len(ids))]
		var predicates []string
		if err := tx.Raw(`SELECT predicate_key FROM relationship_records
			WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND relationship_id=ANY(?::uuid[])`,
			work.TeamID, work.SpaceID, work.Generation, pq.Array(batch)).Scan(&predicates).Error; err != nil {
			return nil, err
		}
		for _, predicate := range predicates {
			keys = append(keys, "predicate:"+predicate)
		}
		after := ""
		for {
			var supports []struct{ ID, Evidence, Ingest, Source, FragmentSource string }
			if err := tx.Raw(`SELECT support.support_id::text AS id,support.fragment_id::text AS evidence,
				fragment.ingest_id::text AS ingest,COALESCE(support.source_id::text,'') AS source,
				COALESCE(fragment.source_id::text,'') AS fragment_source
				FROM relationship_evidence_supports AS support JOIN evidence_fragments AS fragment
				ON fragment.team_id=support.team_id AND fragment.fragment_id=support.fragment_id
				AND fragment.space_id=support.space_id AND fragment.space_generation=support.space_generation
				WHERE support.team_id=?::uuid AND support.space_id=?::uuid AND support.space_generation=?
				AND support.relationship_id=ANY(?::uuid[])
				AND support.support_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
				ORDER BY support.support_id LIMIT ?`, work.TeamID, work.SpaceID, work.Generation,
				pq.Array(batch), after, ontology.TopicProjectionPageSize).Scan(&supports).Error; err != nil {
				return nil, err
			}
			for _, support := range supports {
				keys = append(keys, "evidence:"+support.Evidence, "ingest:"+support.Ingest)
				if support.Source != "" {
					keys = append(keys, "source:"+support.Source)
				}
				if support.FragmentSource != "" {
					keys = append(keys, "source:"+support.FragmentSource)
				}
				after = support.ID
			}
			if len(supports) < ontology.TopicProjectionPageSize {
				break
			}
		}
	}
	return keys, nil
}

func lockProjectionDependencies(tx *gorm.DB, work community.TopicProjectionWork) error {
	keys := make([]string, 0, len(work.Dependencies))
	for key := range work.Dependencies {
		keys = append(keys, key)
	}
	rows, err := tx.Raw(`SELECT live.dependency_key,live.version,dependency.version
		FROM community_topic_versions AS live LEFT JOIN community_topic_dependencies AS dependency
		ON dependency.team_id=live.team_id AND dependency.space_id=live.space_id AND dependency.space_generation=live.space_generation
		AND dependency.dependency_key=live.dependency_key AND dependency.community_id=?::uuid
		WHERE live.team_id=?::uuid AND live.space_id=?::uuid AND live.space_generation=?
		AND (live.dependency_key=ANY(?::text[]) OR dependency.dependency_key IS NOT NULL)
		ORDER BY live.dependency_key FOR SHARE OF live`, work.CommunityID, work.TeamID, work.SpaceID, work.Generation, pq.Array(keys)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var key string
		var live int64
		var stored *int64
		if err := rows.Scan(&key, &live, &stored); err != nil {
			return err
		}
		if expected, ok := work.Dependencies[key]; ok {
			seen++
			if expected != live {
				return community.ErrCommunitySourceStale
			}
		}
		if stored != nil && *stored != live {
			return community.ErrCommunitySourceStale
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen != len(work.Dependencies) {
		return community.ErrCommunitySourceStale
	}
	return nil
}

func communityRecordVisibilitySQL(alias string) string {
	mode := `COALESCE((SELECT value='true' FROM app_config WHERE key='` + domain.AppConfigOntologyEnabled + `'),false)`
	return `((` + alias + `.topic_id IS NULL AND NOT ` + mode + `) OR (` + alias + `.topic_id IS NOT NULL AND ` + mode + `
		AND dense_mem_community_topic_current(` + alias + `.team_id,` + alias + `.community_id)))`
}
