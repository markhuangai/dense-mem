package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

const maxValidationRecords = ontology.MaxDependencyRecords

func decodeRecord(body []byte) (ontology.Record, error) {
	var record ontology.Record
	if err := jsonstrict.Decode(strings.NewReader(string(body)), &record, 65536); err != nil {
		return record, fmt.Errorf("ontology: decode stored record: %w", err)
	}
	return record, ontology.ValidateRecord(record)
}

func catalogRevision(tx *gorm.DB, fence scope) (int64, error) {
	var revision int64
	err := tx.Raw(`SELECT revision FROM ontology_catalog_heads WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

func loadHeads(tx *gorm.DB, fence scope, ids, names, sourceKeys []string) (map[string]ontology.Record, error) {
	rows, err := tx.Raw(`SELECT revision.body
		FROM ontology_record_heads AS head JOIN ontology_record_revisions AS revision
		  USING (team_id,shared_space_id,space_generation,record_id,version)
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
		  AND (head.record_id=ANY(?::uuid[])
		    OR (NOT head.retired AND head.names && ?::text[])
		    OR (NOT head.retired AND head.kind='override' AND (
		      revision.body->'override'->>'target_id'=ANY(?::text[])
		      OR EXISTS (SELECT 1 FROM ontology_source_dependencies AS dependency
		        WHERE dependency.team_id=head.team_id AND dependency.shared_space_id=head.shared_space_id
		          AND dependency.space_generation=head.space_generation AND dependency.record_id=head.record_id
		          AND dependency.record_version=head.version
		          AND dependency.source_kind || ':' || dependency.source_id=ANY(?::text[])))))
		ORDER BY head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids), pq.Array(names), pq.Array(ids), pq.Array(sourceKeys), maxValidationRecords+1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ontology.Record{}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return nil, err
		}
		result[record.ID] = record
	}
	if len(result) > maxValidationRecords {
		return nil, fmt.Errorf("%w: affected validation context exceeds its bound", ontology.ErrContextBound)
	}
	return result, rows.Err()
}

func validationCatalog(tx *gorm.DB, fence scope, records []ontology.Record, changedDefinitions []string) (map[string]ontology.Record, error) {
	var ids, names, keys []string
	if len(changedDefinitions) > 0 {
		var children []string
		err := tx.Raw(`SELECT head.record_id::text
			FROM ontology_revision_dependencies AS dependency
			JOIN ontology_record_heads AS head
			  ON head.team_id=dependency.team_id AND head.shared_space_id=dependency.shared_space_id
			  AND head.space_generation=dependency.space_generation AND head.record_id=dependency.record_id
			  AND head.version=dependency.record_version
			JOIN ontology_record_revisions AS revision
			  ON revision.team_id=head.team_id AND revision.shared_space_id=head.shared_space_id
			  AND revision.space_generation=head.space_generation AND revision.record_id=head.record_id
			  AND revision.version=head.version
			WHERE dependency.team_id=?::uuid AND dependency.shared_space_id=?::uuid AND dependency.space_generation=?
			  AND dependency.dependency_id=ANY(?::uuid[]) AND NOT head.retired
			  AND revision.body->'definition'->>'parent_id'=dependency.dependency_id::text
			ORDER BY head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation,
			pq.Array(changedDefinitions), maxValidationRecords+1).Scan(&children).Error
		if err != nil {
			return nil, err
		}
		if len(children) > maxValidationRecords {
			return nil, fmt.Errorf("%w: affected children exceed validation bound", ontology.ErrContextBound)
		}
		ids = append(ids, children...)
	}
	for _, record := range records {
		ids = append(ids, record.ID)
		ids = append(ids, ontology.ReferenceIDs(record)...)
		names = append(names, ontology.DefinitionNames(record)...)
		for _, source := range ontology.RequiredSources(record) {
			keys = append(keys, ontology.SourceKey(source))
		}
	}
	catalog, err := loadHeads(tx, fence, ids, names, keys)
	if err != nil {
		return nil, err
	}
	requested := map[string]bool{}
	requestedKeys := map[string]bool{}
	for _, id := range ids {
		requested[id] = true
	}
	for _, key := range keys {
		requestedKeys[key] = true
	}
	for depth := 0; depth <= ontology.MaxParentDepth; depth++ {
		pending := []string{}
		pendingKeys := []string{}
		candidates := append([]ontology.Record(nil), records...)
		for _, record := range catalog {
			candidates = append(candidates, record)
		}
		for _, record := range candidates {
			for _, source := range ontology.RequiredSources(record) {
				key := ontology.SourceKey(source)
				if !requestedKeys[key] {
					requestedKeys[key] = true
					pendingKeys = append(pendingKeys, key)
				}
			}
			for _, id := range ontology.ReferenceIDs(record) {
				if !requested[id] {
					requested[id] = true
					pending = append(pending, id)
				}
			}
		}
		if len(pending) == 0 && len(pendingKeys) == 0 {
			return catalog, nil
		}
		loaded, err := loadHeads(tx, fence, pending, nil, pendingKeys)
		if err != nil {
			return nil, err
		}
		for id, record := range loaded {
			catalog[id] = record
		}
		if len(catalog) > maxValidationRecords {
			return nil, fmt.Errorf("%w: dependency context exceeds its bound", ontology.ErrContextBound)
		}
	}
	return nil, fmt.Errorf("%w: dependency depth exceeds its bound", ontology.ErrContextBound)
}

func (s *Store) GetRecord(ctx context.Context, teamID, recordID string, version int64) (ontology.RecordView, error) {
	if _, err := uuid.Parse(recordID); err != nil || version < 0 {
		return ontology.RecordView{}, ontology.ErrInvalid
	}
	var result ontology.RecordView
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var body []byte
		err := tx.Raw(`SELECT revision.body FROM ontology_record_revisions AS revision
			JOIN ontology_record_heads AS head USING (team_id,shared_space_id,space_generation,record_id)
			WHERE revision.team_id=?::uuid AND revision.shared_space_id=?::uuid AND revision.space_generation=?
			  AND revision.record_id=?::uuid AND revision.version=CASE WHEN ?=0 THEN head.version ELSE ? END`, fence.TeamID, fence.SpaceID, fence.Generation, recordID, version, version).Row().Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return ontology.ErrNotFound
		}
		if err != nil {
			return err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return err
		}
		result, err = s.currentView(tx, fence, record)
		return err
	})
	return result, err
}

func (s *Store) currentView(tx *gorm.DB, fence scope, record ontology.Record) (ontology.RecordView, error) {
	view := ontology.RecordView{Record: record}
	if record.Retired {
		view.StaleReason = "retired"
		return view, nil
	}
	catalog, err := validationCatalog(tx, fence, []ontology.Record{record}, nil)
	if err != nil {
		return view, err
	}
	if head, exists := catalog[record.ID]; !exists || head.Version != record.Version {
		view.StaleReason = "historical_revision"
		return view, nil
	}
	snapshots, err := sourceSnapshots(tx, fence, []ontology.Record{record}, catalog)
	if errors.Is(err, ontology.ErrDependencyStale) {
		view.StaleReason = "dependency_changed"
		return view, nil
	}
	if errors.Is(err, ontology.ErrSourceStale) {
		view.StaleReason = "source_changed"
		return view, nil
	}
	if err != nil {
		return view, err
	}
	if err := ontology.CheckSourceDependencies(record, snapshots); err != nil {
		if errors.Is(err, ontology.ErrSourceStale) {
			view.StaleReason = "source_changed"
			return view, nil
		}
		return view, err
	}
	if err := ontology.CheckDependencies([]ontology.Record{record}, snapshots, catalog); err != nil {
		if errors.Is(err, ontology.ErrSourceStale) {
			view.StaleReason = "dependency_changed"
			return view, nil
		}
		return view, err
	}
	fingerprint, err := ontology.RecordFingerprint(record, snapshots, catalog)
	if errors.Is(err, ontology.ErrSourceStale) {
		view.StaleReason = "dependency_changed"
		return view, nil
	}
	if err != nil {
		return view, err
	}
	view.Current = fingerprint == record.Fingerprint
	if !view.Current {
		view.StaleReason = "dependency_changed"
	}
	return view, nil
}

func (s *Store) ListRecords(ctx context.Context, teamID string, kind ontology.Kind, after string, limit int) (ontology.Page, error) {
	if limit < 1 || limit > ontology.MaxPageSize {
		return ontology.Page{}, ontology.ErrInvalid
	}
	if after != "" {
		if _, err := uuid.Parse(after); err != nil {
			return ontology.Page{}, ontology.ErrInvalid
		}
	}
	result := ontology.Page{Records: []ontology.RecordView{}}
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		rows, err := tx.Raw(`SELECT revision.body FROM ontology_record_heads AS head
			JOIN ontology_record_revisions AS revision USING (team_id,shared_space_id,space_generation,record_id,version)
			WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
			  AND (?='' OR head.kind=?) AND (?='' OR head.record_id>NULLIF(?,'')::uuid)
			ORDER BY head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, string(kind), string(kind), after, after, limit+1).Rows()
		if err != nil {
			return err
		}
		var records []ontology.Record
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				_ = rows.Close()
				return err
			}
			record, err := decodeRecord(body)
			if err != nil {
				_ = rows.Close()
				return err
			}
			records = append(records, record)
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if len(records) > limit {
			result.NextID = records[limit-1].ID
			records = records[:limit]
		}
		for _, record := range records {
			view, err := s.currentView(tx, fence, record)
			if err != nil {
				return err
			}
			result.Records = append(result.Records, view)
		}
		result.Revision, err = catalogRevision(tx, fence)
		return err
	})
	return result, err
}

func marshalRecord(record ontology.Record) ([]byte, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(body) > 65536 {
		return nil, fmt.Errorf("%w: record exceeds storage bound", ontology.ErrInvalid)
	}
	return body, nil
}
