package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

type recallReadCache struct {
	pool     gorm.ConnPool
	team     string
	fence    *scope
	records  map[string]topic.Record
	ids      map[string]bool
	sources  map[topic.SourceHandle]topic.SourceSnapshot
	groups   map[string]topic.RecordView
	groupIDs map[string]bool
}

// NewRecallReader keeps reused reads inside the caller's single authoritative transaction.
func (s *Store) NewRecallReader() func(context.Context, *gorm.DB, string, topic.RecallReadInput) (topic.RecallOrganization, error) {
	reader := *s
	return func(ctx context.Context, tx *gorm.DB, team string, input topic.RecallReadInput) (topic.RecallOrganization, error) {
		if tx != nil {
			if _, transactional := tx.Statement.ConnPool.(gorm.TxCommitter); transactional {
				if reader.recallCache == nil || reader.recallCache.pool != tx.Statement.ConnPool || reader.recallCache.team != team {
					reader.recallCache = &recallReadCache{pool: tx.Statement.ConnPool, team: team, records: map[string]topic.Record{}, ids: map[string]bool{}, sources: map[topic.SourceHandle]topic.SourceSnapshot{}, groups: map[string]topic.RecordView{}, groupIDs: map[string]bool{}}
				}
			} else {
				reader.recallCache = nil
			}
		}
		return reader.ReadRecall(ctx, tx, team, input)
	}
}

func (c *recallReadCache) loadHeads(tx *gorm.DB, fence scope, ids []string) (map[string]topic.Record, error) {
	var newIDs []string
	for _, id := range ids {
		if !c.ids[id] {
			newIDs = append(newIDs, id)
		}
	}
	if len(newIDs) > 0 {
		loaded, err := loadHeads(tx, fence, newIDs, nil)
		if err != nil {
			return nil, err
		}
		for id, record := range loaded {
			c.records[id], c.ids[id] = record, true
		}
		for _, id := range newIDs {
			c.ids[id] = true
		}

	}
	if len(c.records) > topic.MaxDependencyRecords {
		return nil, topic.ErrContextBound
	}
	result := make(map[string]topic.Record, len(c.records))
	for id, record := range c.records {
		result[id] = record
	}
	return result, nil
}

func (c *recallReadCache) readSources(tx *gorm.DB, fence scope, handles []topic.SourceHandle) (map[topic.SourceHandle]topic.SourceSnapshot, error) {
	missing := []topic.SourceHandle{}
	for _, handle := range handles {
		if _, exists := c.sources[handle]; !exists {
			missing = append(missing, handle)
		}
	}
	if len(missing) > 0 {
		loaded, err := readSources(tx, fence, missing)
		if err != nil {
			return nil, err
		}
		for handle, snapshot := range loaded {
			c.sources[handle] = snapshot
		}
		for _, handle := range missing {
			if _, exists := c.sources[handle]; !exists {
				c.sources[handle] = topic.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation}
			}
		}
	}
	if len(c.sources) > topic.MaxDependencyRecords {
		return nil, topic.ErrContextBound
	}
	return c.sources, nil
}

// ReadRecall uses Recall's transaction so organization and hydrated sources share its visibility snapshot.
func (s *Store) ReadRecall(ctx context.Context, tx *gorm.DB, teamID string, input topic.RecallReadInput) (topic.RecallOrganization, error) {
	result := topic.RecallOrganization{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if tx == nil || input.Limit < 1 || input.Limit > topic.MaxPageSize || len(input.EvidenceIDs) > topic.MaxDependencyRecords || len(input.CandidateEvidenceIDs) > topic.MaxDependencyRecords {
		return result, topic.ErrInvalid
	}
	var fence scope
	var err error
	if s.recallCache != nil && s.recallCache.fence != nil {
		fence = *s.recallCache.fence
		err = authorizeSharedScope(ctx, fence)
	} else {
		fence, err = sharedScope(ctx, tx, teamID)
		if err == nil && s.recallCache != nil {
			s.recallCache.fence = &fence
		}
	}
	if err != nil {
		return result, err
	}
	if fence.TeamID != teamID {
		return result, topic.ErrUnauthorized
	}
	if input.SpaceID != "" && input.SpaceID != fence.SpaceID {
		return result, topic.ErrUnauthorized
	}
	if len(input.EvidenceIDs) > 0 {
		return s.readRecallGroups(tx, fence, input)
	}
	names, bounded := topic.RecallQueryNames(input.Query)
	if bounded {
		result.Degradation = "ontology_bound_exceeded"
		return result, nil
	}
	if len(names) == 0 {
		return result, nil
	}
	for _, id := range input.CandidateEvidenceIDs {
		if err := topic.ValidateSourceHandle(topic.SourceHandle{Kind: topic.EvidenceSource, ID: id, Version: 1}); err != nil {
			return result, err
		}
	}
	records, err := recallDiscoveryRecords(tx, fence, names, input.CandidateEvidenceIDs, input.Limit)
	if err != nil {
		return result, err
	}
	views, err := s.currentViews(tx, fence, records)
	if err != nil {
		return result, err
	}
	if s.recallCache != nil && input.CandidateEvidenceIDs != nil {
		covered := append([]string(nil), input.CandidateEvidenceIDs...)
		for _, view := range views {
			if view.Assignment != nil && view.Assignment.Source.Kind == topic.EvidenceSource {
				covered = append(covered, view.Assignment.Source.ID)
			}
		}
		if err := s.recallCache.cacheGroups(views, covered); err != nil {
			return result, err
		}
	}
	matched := topic.MatchRecallDefinitions(names, views)
	if len(matched) > topic.MaxVocabularyCandidates {
		result.Degradation = "ontology_bound_exceeded"
		return result, nil
	}
	if len(matched) == 0 {
		for _, view := range views {
			if view.Definition != nil && !view.Current {
				result.Degradation = "ontology_stale"
			}
		}
		return result, nil
	}
	definitions := map[string]bool{}
	for _, id := range matched {
		definitions[id] = true
	}
	seen := map[string]bool{}
	for _, view := range views {
		if view.Assignment == nil || !definitions[view.Assignment.DefinitionID] {
			continue
		}
		if !view.Current {
			result.Degradation = "ontology_stale"
			continue
		}
		if view.Assignment != nil && !seen[topic.SourceKey(view.Assignment.Source)] {
			result.Sources = append(result.Sources, view.Assignment.Source)
			seen[topic.SourceKey(view.Assignment.Source)] = true
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		return topic.SourceKey(result.Sources[i]) < topic.SourceKey(result.Sources[j])
	})
	return result, nil
}

func (s *Store) readRecallGroups(tx *gorm.DB, fence scope, input topic.RecallReadInput) (topic.RecallOrganization, error) {
	result := topic.RecallOrganization{}
	requested := map[string]bool{}
	missing := []string{}
	for _, id := range input.EvidenceIDs {
		if err := topic.ValidateSourceHandle(topic.SourceHandle{Kind: topic.EvidenceSource, ID: id, Version: 1}); err != nil {
			return result, err
		}
		requested[id] = true
		if s.recallCache == nil || !s.recallCache.groupIDs[id] {
			missing = append(missing, id)
		}
	}
	var views []topic.RecordView
	if len(missing) > 0 {
		records, err := recallRecords(tx, fence, `head.kind='evidence_group' AND EXISTS (
        SELECT 1 FROM evidence_equivalences AS peer
        WHERE (peer.team_id,peer.shared_space_id,peer.space_generation,peer.record_id,peer.version)=
            (head.team_id,head.shared_space_id,head.space_generation,head.record_id,head.version)
        AND NOT peer.retired AND peer.members && ?::uuid[])`, []any{pq.Array(missing)}, topic.MaxPageSize)
		if err != nil {
			return result, err
		}
		views, err = s.currentViews(tx, fence, records)
		if err != nil {
			return result, err
		}
		if s.recallCache != nil {
			if err := s.recallCache.cacheGroups(views, missing); err != nil {
				return result, err
			}
		}
	}
	if s.recallCache != nil {
		views = nil
		for _, view := range s.recallCache.groups {
			for _, member := range view.Group.Members {
				if requested[member.ID] {
					views = append(views, view)
					break
				}
			}
		}
		sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	}
	for _, view := range views {
		if !view.Current {
			result.Degradation = "ontology_stale"
			continue
		}
		group := topic.RecallEvidenceGroup{ID: view.ID}
		for _, member := range view.Group.Members {
			group.Members = append(group.Members, member.ID)
		}
		sort.Strings(group.Members)
		result.Groups = append(result.Groups, group)
	}
	return result, nil
}

func (c *recallReadCache) cacheGroups(views []topic.RecordView, ids []string) error {
	for _, view := range views {
		if view.Kind == topic.EvidenceGroup {
			c.groups[view.ID] = view
		}
	}
	if len(c.groups) > topic.MaxPageSize {
		return topic.ErrContextBound
	}
	for _, id := range ids {
		c.groupIDs[id] = true
	}
	return nil
}

// Keep revision lookups correlated so sparse table statistics cannot turn them into scope-wide body scans.
const recallRevisionJoinSQL = `JOIN LATERAL (
	SELECT revision.body FROM topic_revision_records AS revision
	WHERE revision.team_id=head.team_id AND revision.shared_space_id=head.shared_space_id
	AND revision.space_generation=head.space_generation AND revision.record_id=head.record_id AND revision.version=head.version
	LIMIT 1
) AS revision ON TRUE`

func recallRecords(tx *gorm.DB, fence scope, predicate string, args []any, limit int) ([]topic.Record, error) {
	parameters := append([]any{fence.TeamID, fence.SpaceID, fence.Generation}, args...)
	parameters = append(parameters, limit+1)
	rows, err := tx.Raw(`SELECT revision.body FROM topic_record_heads AS head
		`+recallRevisionJoinSQL+`
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
		AND NOT head.retired AND `+predicate+` ORDER BY head.record_id LIMIT ?`, parameters...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []topic.Record{}
	decoder := newStoredRecordDecoder()
	for rows.Next() {
		var body sql.RawBytes
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		record, err := decoder.decode(body)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if len(result) > limit {
		return nil, fmt.Errorf("%w: recall organization records", topic.ErrContextBound)
	}
	return result, rows.Err()
}

func recallDiscoveryRecords(tx *gorm.DB, fence scope, names, evidenceIDs []string, limit int) ([]topic.Record, error) {
	rows, err := tx.Raw(`WITH scope AS (
		SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation,?::text[] AS names,?::text[] AS evidence_ids,?::boolean AS preload_groups
	), definitions AS (
		SELECT head.record_id,revision.body FROM topic_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE NOT head.retired AND head.kind IN ('topic') AND head.names && scope.names
		ORDER BY head.record_id LIMIT ?
	), assignments AS (
		SELECT head.record_id,revision.body FROM topic_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE NOT head.retired AND head.kind='assignment' AND EXISTS (
			SELECT 1 FROM topic_revision_dependencies AS dependency
			WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
			AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
			AND dependency.record_version=head.version
			AND dependency.dependency_id=ANY(ARRAY(SELECT record_id FROM definitions)))
		ORDER BY head.record_id LIMIT ?
	), selected_evidence AS (
		SELECT unnest(evidence_ids) AS source_id FROM scope
		UNION
		SELECT body->'assignment'->'source'->>'id' FROM assignments
		WHERE body->'assignment'->'source'->>'kind'='evidence'
	), groups AS (
		SELECT head.record_id,revision.body FROM topic_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE scope.preload_groups AND NOT head.retired AND head.kind='evidence_group' AND EXISTS (
			SELECT 1 FROM topic_source_dependencies AS dependency
			WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
			AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
			AND dependency.record_version=head.version AND dependency.source_kind='evidence'
			AND dependency.source_id=ANY(ARRAY(SELECT source_id FROM selected_evidence)))
		ORDER BY head.record_id LIMIT ?
	), roots AS (
		SELECT record_id,body FROM definitions UNION ALL SELECT record_id,body FROM assignments UNION ALL SELECT record_id,body FROM groups
	) SELECT body FROM roots`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(names), pq.Array(evidenceIDs), evidenceIDs != nil, topic.MaxPageSize+1, limit+1, topic.MaxPageSize+1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []topic.Record{}
	decoder := newStoredRecordDecoder()
	definitions, assignments, groups := 0, 0, 0
	for rows.Next() {
		var body sql.RawBytes
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		record, err := decoder.decode(body)
		if err != nil {
			return nil, err
		}
		if record.Kind == topic.EvidenceGroup {
			groups++
		} else if record.Assignment == nil {
			definitions++
		} else {
			assignments++
		}
		result = append(result, record)
	}
	if definitions > topic.MaxPageSize || assignments > limit || groups > topic.MaxPageSize || len(result) > topic.MaxDependencyRecords {
		return nil, topic.ErrContextBound
	}
	return result, rows.Err()
}
