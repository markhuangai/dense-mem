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

func projectionSourceKeys(tx *gorm.DB, work *community.TopicProjectionWork, page ontology.TopicMembershipPage) ([]string, error) {
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
		case ontology.PredicateSource:
			keys = append(keys, "predicate-membership:"+source.ID+"@"+strconv.FormatInt(source.Version, 10))
		}
	}
	expanded, cursor, pending, err := projectionExpandedSourceKeys(tx, *work, page.Sources)
	if err != nil {
		return nil, err
	}
	work.DependencyCursor, work.DependenciesPending = cursor, pending
	keys = append(keys, expanded...)
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

func projectionExpandedSourceKeys(tx *gorm.DB, work community.TopicProjectionWork, sources []ontology.SourceHandle) ([]string, community.TopicProjectionDependencyCursor, bool, error) {
	ordered := append([]ontology.SourceHandle(nil), sources...)
	key := func(source ontology.SourceHandle) string {
		return ontology.SourceKey(source) + "@" + strconv.FormatInt(source.Version, 10)
	}
	sort.Slice(ordered, func(i, j int) bool { return key(ordered[i]) < key(ordered[j]) })
	cursor, remaining, keys := work.DependencyCursor, ontology.TopicProjectionPageSize, []string{}
	for _, source := range ordered {
		if source.Kind != ontology.PredicateSource && source.Kind != ontology.RelationshipSource {
			continue
		}
		sourceKey := key(source)
		if sourceKey < cursor.SourceKey || (sourceKey == cursor.SourceKey && cursor.RelationshipID == "") {
			continue
		}
		if remaining == 0 {
			return keys, cursor, true, nil
		}
		if sourceKey != cursor.SourceKey {
			cursor = community.TopicProjectionDependencyCursor{SourceKey: sourceKey}
		}
		for remaining > 0 {
			var relationship struct{ ID, Predicate string }
			query := `SELECT relationship_id::text AS id,predicate_key AS predicate FROM relationship_records
				WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=?`
			args := []any{work.TeamID, work.SpaceID, work.Generation}
			if source.Kind == ontology.PredicateSource && cursor.SupportID == "" {
				query += ` AND predicate_key=? AND predicate_version=?
					AND relationship_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
					ORDER BY relationship_id LIMIT 1`
				args = append(args, source.ID, source.Version, cursor.RelationshipID)
			} else {
				id := source.ID
				if source.Kind == ontology.PredicateSource {
					id = cursor.RelationshipID
				}
				query += ` AND relationship_id=?::uuid`
				args = append(args, id)
			}
			if err := tx.Raw(query, args...).Scan(&relationship).Error; err != nil {
				return nil, cursor, false, err
			}
			if relationship.ID == "" {
				cursor.RelationshipID, cursor.SupportID = "", ""
				break
			}
			keys = append(keys, "relationship:"+relationship.ID, "predicate:"+relationship.Predicate)
			remaining--
			cursor.RelationshipID = relationship.ID
			if cursor.SupportID == "" {
				// Zero resumes this relationship before its first support; empty advances to the next relationship.
				cursor.SupportID = "00000000-0000-0000-0000-000000000000"
			}
			var supports []struct{ ID, Evidence, Ingest, Source, FragmentSource string }
			if err := tx.Raw(`SELECT support.support_id::text AS id,support.fragment_id::text AS evidence,
				fragment.ingest_id::text AS ingest,COALESCE(support.source_id::text,'') AS source,
				COALESCE(fragment.source_id::text,'') AS fragment_source
				FROM relationship_evidence_supports AS support JOIN evidence_fragments AS fragment
				ON fragment.team_id=support.team_id AND fragment.fragment_id=support.fragment_id
				AND fragment.space_id=support.space_id AND fragment.space_generation=support.space_generation
				WHERE support.team_id=?::uuid AND support.space_id=?::uuid AND support.space_generation=?
				AND support.relationship_id=?::uuid AND support.support_id>?::uuid ORDER BY support.support_id LIMIT ?`,
				work.TeamID, work.SpaceID, work.Generation, relationship.ID, cursor.SupportID, remaining+1).Scan(&supports).Error; err != nil {
				return nil, cursor, false, err
			}
			more := len(supports) > remaining
			for _, support := range supports[:min(len(supports), remaining)] {
				keys = append(keys, "evidence:"+support.Evidence, "ingest:"+support.Ingest)
				if support.Source != "" {
					keys = append(keys, "source:"+support.Source)
				}
				if support.FragmentSource != "" {
					keys = append(keys, "source:"+support.FragmentSource)
				}
				cursor.SupportID = support.ID
			}
			remaining -= min(len(supports), remaining)
			if more {
				return keys, cursor, true, nil
			}
			cursor.SupportID = ""
			if source.Kind == ontology.RelationshipSource {
				cursor.RelationshipID = ""
				break
			}
		}
		if remaining == 0 && cursor.RelationshipID != "" {
			return keys, cursor, true, nil
		}
	}
	return keys, community.TopicProjectionDependencyCursor{}, false, nil
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

const communityOntologyModeSQL = `COALESCE((SELECT value='true' FROM app_config WHERE key='` + domain.AppConfigOntologyEnabled + `'),false)`

func communityRecordVisibilitySQL(alias string) string {
	mode := communityOntologyModeSQL
	return `((` + alias + `.topic_id IS NULL AND NOT ` + mode + `) OR (` + alias + `.topic_id IS NOT NULL AND ` + mode + `
		AND dense_mem_community_topic_current(` + alias + `.team_id,` + alias + `.community_id)))`
}
