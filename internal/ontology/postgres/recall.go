package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

type recallReadCache struct {
	pool     gorm.ConnPool
	team     string
	fence    *scope
	records  map[string]ontology.Record
	ids      map[string]bool
	keys     map[string]bool
	sources  map[ontology.SourceHandle]ontology.SourceSnapshot
	groups   map[string]ontology.RecordView
	groupIDs map[string]bool
}

// NewRecallReader keeps reused reads inside the caller's single authoritative transaction.
func (s *Store) NewRecallReader() func(context.Context, *gorm.DB, string, ontology.RecallReadInput) (ontology.RecallOrganization, error) {
	reader := *s
	return func(ctx context.Context, tx *gorm.DB, team string, input ontology.RecallReadInput) (ontology.RecallOrganization, error) {
		if tx != nil {
			if _, transactional := tx.Statement.ConnPool.(gorm.TxCommitter); transactional {
				if reader.recallCache == nil || reader.recallCache.pool != tx.Statement.ConnPool || reader.recallCache.team != team {
					reader.recallCache = &recallReadCache{pool: tx.Statement.ConnPool, team: team, records: map[string]ontology.Record{}, ids: map[string]bool{}, keys: map[string]bool{}, sources: map[ontology.SourceHandle]ontology.SourceSnapshot{}, groups: map[string]ontology.RecordView{}, groupIDs: map[string]bool{}}
				}
			} else {
				reader.recallCache = nil
			}
		}
		return reader.ReadRecall(ctx, tx, team, input)
	}
}

func (c *recallReadCache) loadHeads(tx *gorm.DB, fence scope, ids, keys []string) (map[string]ontology.Record, error) {
	missing := func(values []string, covered map[string]bool) []string {
		result := []string{}
		for _, value := range values {
			if !covered[value] {
				result = append(result, value)
			}
		}
		return result
	}
	newIDs, newKeys := missing(ids, c.ids), missing(keys, c.keys)
	if len(newIDs)+len(newKeys) > 0 {
		loaded, err := loadHeads(tx, fence, newIDs, nil, newKeys)
		if err != nil {
			return nil, err
		}
		for id, record := range loaded {
			c.records[id], c.ids[id] = record, true
		}
		for _, id := range newIDs {
			c.ids[id] = true
		}
		for _, key := range newKeys {
			c.keys[key] = true
		}
	}
	if len(c.records) > ontology.MaxDependencyRecords {
		return nil, ontology.ErrContextBound
	}
	result := make(map[string]ontology.Record, len(c.records))
	for id, record := range c.records {
		result[id] = record
	}
	return result, nil
}

func (c *recallReadCache) readSources(tx *gorm.DB, fence scope, handles []ontology.SourceHandle) (map[ontology.SourceHandle]ontology.SourceSnapshot, error) {
	missing := []ontology.SourceHandle{}
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
				c.sources[handle] = ontology.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation}
			}
		}
	}
	if len(c.sources) > ontology.MaxDependencyRecords {
		return nil, ontology.ErrContextBound
	}
	return c.sources, nil
}

// ReadRecall uses Recall's transaction so organization and hydrated sources share its visibility snapshot.
func (s *Store) ReadRecall(ctx context.Context, tx *gorm.DB, teamID string, input ontology.RecallReadInput) (ontology.RecallOrganization, error) {
	result := ontology.RecallOrganization{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if tx == nil || input.Limit < 1 || input.Limit > ontology.MaxPageSize || len(input.EvidenceIDs) > ontology.MaxDependencyRecords || len(input.CandidateEvidenceIDs) > ontology.MaxDependencyRecords {
		return result, ontology.ErrInvalid
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
		return result, ontology.ErrUnauthorized
	}
	if input.SpaceID != "" && input.SpaceID != fence.SpaceID {
		return result, ontology.ErrUnauthorized
	}
	if len(input.EvidenceIDs) > 0 {
		return s.readRecallGroups(tx, fence, input)
	}
	names, bounded := ontology.RecallQueryNames(input.Query)
	if bounded {
		result.Degradation = "ontology_bound_exceeded"
		return result, nil
	}
	if len(names) == 0 {
		return result, nil
	}
	for _, id := range input.CandidateEvidenceIDs {
		if err := ontology.ValidateSourceHandle(ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: id, Version: 1}); err != nil {
			return result, err
		}
	}
	records, err := recallDiscoveryRecords(tx, fence, names, input.CandidateEvidenceIDs, input.Limit)
	if err != nil {
		return result, err
	}
	if s.recallCache != nil {
		for _, record := range records {
			if record.Override != nil {
				continue
			}
			for _, source := range ontology.RequiredSources(record) {
				s.recallCache.keys[ontology.SourceKey(source)] = true
			}
		}
	}
	views, err := s.currentViews(tx, fence, records)
	if err != nil {
		return result, err
	}
	if s.recallCache != nil && input.CandidateEvidenceIDs != nil {
		covered := append([]string(nil), input.CandidateEvidenceIDs...)
		for _, view := range views {
			if view.Assignment != nil && view.Assignment.Source.Kind == ontology.EvidenceSource {
				covered = append(covered, view.Assignment.Source.ID)
			}
		}
		if err := s.recallCache.cacheGroups(views, covered); err != nil {
			return result, err
		}
	}
	matched := ontology.MatchRecallDefinitions(names, views)
	if len(matched) > ontology.MaxVocabularyCandidates {
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
		if view.Assignment != nil && !seen[ontology.SourceKey(view.Assignment.Source)] {
			result.Sources = append(result.Sources, view.Assignment.Source)
			seen[ontology.SourceKey(view.Assignment.Source)] = true
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		return ontology.SourceKey(result.Sources[i]) < ontology.SourceKey(result.Sources[j])
	})
	return result, nil
}

func (s *Store) readRecallGroups(tx *gorm.DB, fence scope, input ontology.RecallReadInput) (ontology.RecallOrganization, error) {
	result := ontology.RecallOrganization{}
	requested := map[string]bool{}
	missing := []string{}
	for _, id := range input.EvidenceIDs {
		if err := ontology.ValidateSourceHandle(ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: id, Version: 1}); err != nil {
			return result, err
		}
		requested[id] = true
		if s.recallCache == nil || !s.recallCache.groupIDs[id] {
			missing = append(missing, id)
		}
	}
	var views []ontology.RecordView
	if len(missing) > 0 {
		records, err := recallRecords(tx, fence, `head.kind='evidence_group' AND EXISTS (
		SELECT 1 FROM ontology_source_dependencies AS dependency
		WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
		AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
		AND dependency.record_version=head.version AND dependency.source_kind='evidence'
		AND dependency.source_id=ANY(?::text[]))`, []any{pq.Array(missing)}, ontology.MaxPageSize)
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
		group := ontology.RecallEvidenceGroup{ID: view.ID}
		for _, member := range view.Group.Members {
			group.Members = append(group.Members, member.ID)
		}
		sort.Strings(group.Members)
		result.Groups = append(result.Groups, group)
	}
	return result, nil
}

func (c *recallReadCache) cacheGroups(views []ontology.RecordView, ids []string) error {
	for _, view := range views {
		if view.Kind == ontology.EvidenceGroup {
			c.groups[view.ID] = view
		}
	}
	if len(c.groups) > ontology.MaxPageSize {
		return ontology.ErrContextBound
	}
	for _, id := range ids {
		c.groupIDs[id] = true
	}
	return nil
}

// Keep revision lookups correlated so sparse table statistics cannot turn them into scope-wide body scans.
const recallRevisionJoinSQL = `JOIN LATERAL (
	SELECT revision.body FROM ontology_record_revisions AS revision
	WHERE revision.team_id=head.team_id AND revision.shared_space_id=head.shared_space_id
	AND revision.space_generation=head.space_generation AND revision.record_id=head.record_id AND revision.version=head.version
	LIMIT 1
) AS revision ON TRUE`

func recallRecords(tx *gorm.DB, fence scope, predicate string, args []any, limit int) ([]ontology.Record, error) {
	parameters := append([]any{fence.TeamID, fence.SpaceID, fence.Generation}, args...)
	parameters = append(parameters, limit+1)
	rows, err := tx.Raw(`SELECT revision.body FROM ontology_record_heads AS head
		`+recallRevisionJoinSQL+`
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
		AND NOT head.retired AND `+predicate+` ORDER BY head.record_id LIMIT ?`, parameters...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ontology.Record{}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if len(result) > limit {
		return nil, fmt.Errorf("%w: recall organization records", ontology.ErrContextBound)
	}
	return result, rows.Err()
}

func recallDiscoveryRecords(tx *gorm.DB, fence scope, names, evidenceIDs []string, limit int) ([]ontology.Record, error) {
	rows, err := tx.Raw(`WITH scope AS (
		SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation,?::text[] AS names,?::text[] AS evidence_ids,?::boolean AS preload_groups
	), definitions AS (
		SELECT head.record_id,revision.body FROM ontology_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE NOT head.retired AND head.kind IN ('entity_class','predicate_concept','topic') AND head.names && scope.names
		ORDER BY head.record_id LIMIT ?
	), assignments AS (
		SELECT head.record_id,revision.body FROM ontology_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE NOT head.retired AND head.kind='assignment' AND EXISTS (
			SELECT 1 FROM ontology_revision_dependencies AS dependency JOIN definitions ON definitions.record_id=dependency.dependency_id
			WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
			AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id AND dependency.record_version=head.version)
		ORDER BY head.record_id LIMIT ?
	), selected_evidence AS (
		SELECT unnest(evidence_ids) AS source_id FROM scope
		UNION
		SELECT body->'assignment'->'source'->>'id' FROM assignments
		WHERE body->'assignment'->'source'->>'kind'='evidence'
	), groups AS (
		SELECT head.record_id,revision.body FROM ontology_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE scope.preload_groups AND NOT head.retired AND head.kind='evidence_group' AND EXISTS (
			SELECT 1 FROM ontology_source_dependencies AS dependency JOIN selected_evidence USING(source_id)
			WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
			AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
			AND dependency.record_version=head.version AND dependency.source_kind='evidence')
		ORDER BY head.record_id LIMIT ?
	), roots AS (
		SELECT record_id,body FROM definitions UNION ALL SELECT record_id,body FROM assignments UNION ALL SELECT record_id,body FROM groups
	), source_keys AS (
		SELECT body->'assignment'->'source'->>'kind' || ':' || (body->'assignment'->'source'->>'id') AS source_key
		FROM roots WHERE body->'assignment' IS NOT NULL
		UNION
		SELECT member->>'kind' || ':' || (member->>'id') FROM roots
		CROSS JOIN LATERAL jsonb_array_elements(COALESCE(body->'group'->'members','[]'::jsonb)) AS member
	), overrides AS (
		SELECT head.record_id,revision.body FROM ontology_record_heads AS head
		`+recallRevisionJoinSQL+`
		JOIN scope USING(team_id,shared_space_id,space_generation)
		WHERE NOT head.retired AND head.kind='override' AND (
			revision.body->'override'->>'target_id' IN (SELECT record_id::text FROM roots)
			OR EXISTS (SELECT 1 FROM ontology_source_dependencies AS dependency JOIN source_keys
				ON source_keys.source_key=dependency.source_kind || ':' || dependency.source_id
				WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
				AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
				AND dependency.record_version=head.version))
		ORDER BY head.record_id LIMIT ?
	) SELECT body FROM roots UNION ALL SELECT body FROM overrides`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(names), pq.Array(evidenceIDs), evidenceIDs != nil, ontology.MaxPageSize+1, limit+1, ontology.MaxPageSize+1, ontology.MaxDependencyRecords+1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ontology.Record{}
	definitions, assignments, groups := 0, 0, 0
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return nil, err
		}
		if record.Kind == ontology.OverrideKind {
			if len(result) >= ontology.MaxDependencyRecords {
				return nil, ontology.ErrContextBound
			}
		} else if record.Kind == ontology.EvidenceGroup {
			groups++
		} else if record.Assignment == nil {
			definitions++
		} else {
			assignments++
		}
		result = append(result, record)
	}
	if definitions > ontology.MaxPageSize || assignments > limit || groups > ontology.MaxPageSize || len(result) > ontology.MaxDependencyRecords {
		return nil, ontology.ErrContextBound
	}
	return result, rows.Err()
}
