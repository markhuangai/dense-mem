package postgres

import (
	"context"
	"sort"

	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) PreviewManagementDraft(ctx context.Context, teamID, id string, version int64) (ontology.ImpactPreview, error) {
	var result ontology.ImpactPreview
	if version < 1 {
		return result, ontology.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, owner string) error {
		draft, err := loadManagementDraft(tx, fence, owner, id, false)
		if err != nil {
			return err
		}
		if draft.Draft.State != "open" || draft.Draft.Version != version {
			return ontology.ErrConflict
		}
		if _, _, err := validateManagementPublication(tx, fence, draft.Prepared); err != nil {
			return err
		}
		result = ontology.ImpactPreview{DraftID: id, DraftVersion: version, Revision: draft.Draft.BaseRevision, ChangedRecords: len(draft.Prepared.Changes), Examples: []ontology.ManagementSource{}, Complete: true, RequiresReassessment: true}
		return readManagementImpact(tx, fence, draft.Prepared, &result)
	})
	return result, err
}

const managementImpactSQL = `WITH RECURSIVE scope AS (
 SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation
), roots AS (
 SELECT unnest(?::uuid[]) AS record_id
 UNION SELECT head.record_id FROM ontology_record_heads AS head JOIN scope USING(team_id,shared_space_id,space_generation)
 JOIN ontology_source_dependencies AS source USING(team_id,shared_space_id,space_generation,record_id)
 WHERE head.version=source.record_version AND NOT head.retired
 AND head.kind IN ('assignment','evidence_group','relationship_group','override')
 AND source.source_kind || ':' || source.source_id=ANY(?::text[])
), affected AS (
 SELECT record_id FROM roots
 UNION SELECT head.record_id FROM ontology_revision_dependencies AS dependency JOIN scope USING(team_id,shared_space_id,space_generation)
 JOIN ontology_record_heads AS head ON head.team_id=dependency.team_id AND head.shared_space_id=dependency.shared_space_id
 AND head.space_generation=dependency.space_generation AND head.record_id=dependency.record_id AND head.version=dependency.record_version
 JOIN affected ON dependency.dependency_id=affected.record_id WHERE NOT head.retired
), keys AS (
 SELECT dependency.source_kind,dependency.source_id,dependency.source_version FROM affected
 JOIN ontology_source_dependencies AS dependency USING(record_id) JOIN scope USING(team_id,shared_space_id,space_generation)
 JOIN ontology_record_heads AS head ON head.team_id=dependency.team_id AND head.shared_space_id=dependency.shared_space_id
 AND head.space_generation=dependency.space_generation AND head.record_id=dependency.record_id AND head.version=dependency.record_version
 WHERE NOT head.retired
)
`

func readManagementImpact(tx *gorm.DB, fence scope, input ontology.Publication, result *ontology.ImpactPreview) error {
	var ids, keys []string
	handles := map[ontology.SourceHandle]bool{}
	for _, change := range input.Changes {
		ids = append(ids, change.Record.ID)
		for _, source := range change.Record.Sources {
			keys = append(keys, ontology.SourceKey(source.SourceHandle))
			handles[source.SourceHandle] = true
		}
	}
	args := []any{fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids), pq.Array(keys)}
	if err := tx.Raw(managementImpactSQL+`SELECT count(*) FROM affected JOIN ontology_record_heads AS head USING(record_id) JOIN scope USING(team_id,shared_space_id,space_generation) WHERE NOT head.retired`, args...).Row().Scan(&result.AffectedRecords); err != nil {
		return err
	}
	rows, err := tx.Raw(managementImpactSQL+`SELECT DISTINCT source_kind,source_id,source_version FROM keys ORDER BY source_kind,source_id,source_version LIMIT ?`, append(args, ontology.MaxDependencyRecords+1)...).Rows()
	if err != nil {
		return err
	}
	for rows.Next() {
		var handle ontology.SourceHandle
		if err := rows.Scan(&handle.Kind, &handle.ID, &handle.Version); err != nil {
			rows.Close()
			return err
		}
		handles[handle] = true
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if len(handles) > ontology.MaxDependencyRecords {
		return ontology.ErrContextBound
	}
	ordered := make([]ontology.SourceHandle, 0, len(handles))
	for handle := range handles {
		ordered = append(ordered, handle)
	}
	snapshots, err := readSources(tx, fence, ordered)
	if err != nil {
		return err
	}
	// A stale dependency handle must not turn a preview example into evidence disclosure.
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ontology.SourceKey(ordered[i]), ontology.SourceKey(ordered[j])
		if left != right {
			return left < right
		}
		return ordered[i].Version < ordered[j].Version
	})
	for _, handle := range ordered {
		source, ok := snapshots[handle]
		if !ok || !source.Eligible {
			continue
		}
		result.AffectedSources++
		if len(result.Examples) < ontology.MaxPreviewExamples {
			result.Examples = append(result.Examples, managementSource(source))
		}
	}
	result.ExamplesTruncated = result.AffectedSources > int64(len(result.Examples))
	return nil
}
