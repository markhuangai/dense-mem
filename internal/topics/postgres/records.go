package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

const maxValidationRecords = topic.MaxDependencyRecords

func decodeRecord(body []byte) (topic.Record, error) {
	return newStoredRecordDecoder().decode(body)
}

type storedRecordDecoder struct {
	reader  *bytes.Reader
	decoder *json.Decoder
}

func newStoredRecordDecoder() *storedRecordDecoder {
	reader := bytes.NewReader(nil)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	return &storedRecordDecoder{reader: reader, decoder: decoder}
}

func (d *storedRecordDecoder) decode(body []byte) (topic.Record, error) {
	var record topic.Record
	if len(body) > 65536 {
		return record, fmt.Errorf("topics: stored record exceeds byte bound")
	}
	// JSONB has already normalized duplicate keys in stored record bodies.
	d.reader.Reset(body)
	if err := d.decoder.Decode(&record); err != nil {
		return record, fmt.Errorf("topics: decode stored record: %w", err)
	}
	trailing, err := io.ReadAll(d.decoder.Buffered())
	if err != nil {
		return record, fmt.Errorf("topics: decode stored record: %w", err)
	}
	if len(bytes.Trim(trailing, " \t\r\n")) != 0 || len(bytes.Trim(body[len(body)-d.reader.Len():], " \t\r\n")) != 0 {
		return record, fmt.Errorf("topics: stored body must contain one JSON record")
	}
	return record, topic.ValidateRecord(record)
}

func catalogRevision(tx *gorm.DB, fence scope) (int64, error) {
	var revision int64
	err := tx.Raw(`SELECT revision FROM topic_catalog_heads WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		notePublicationRevision(tx, 0)
		return 0, nil
	}
	if err == nil {
		notePublicationRevision(tx, revision)
	}
	return revision, err
}

func loadHeads(tx *gorm.DB, fence scope, ids, names []string) (map[string]topic.Record, error) {
	rows, err := tx.Raw(`SELECT revision.body
		FROM topic_record_heads AS head JOIN topic_revision_records AS revision
		  USING (team_id,shared_space_id,space_generation,record_id,version)
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
		  AND (head.record_id=ANY(?::uuid[])
		    OR (NOT head.retired AND head.names && ?::text[]))
		ORDER BY head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids), pq.Array(names), maxValidationRecords+1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]topic.Record{}
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
		return nil, fmt.Errorf("%w: affected validation context exceeds its bound", topic.ErrContextBound)
	}
	return result, rows.Err()
}

func validationCatalog(tx *gorm.DB, fence scope, records []topic.Record, changedDefinitions []string) (map[string]topic.Record, error) {
	return validationCatalogWithLoader(tx, fence, records, changedDefinitions, func(ids, names []string) (map[string]topic.Record, error) {
		loaded, err := loadHeads(tx, fence, ids, names)
		notePublicationDefinitions(tx, loaded)
		return loaded, err
	})
}

func validationCatalogWithLoader(tx *gorm.DB, fence scope, records []topic.Record, changedDefinitions []string, load func([]string, []string) (map[string]topic.Record, error)) (map[string]topic.Record, error) {
	var ids, names []string
	if len(changedDefinitions) > 0 {
		var children []string
		err := tx.Raw(`SELECT head.record_id::text
			FROM topic_revision_dependencies AS dependency
			JOIN topic_record_heads AS head
			  ON head.team_id=dependency.team_id AND head.shared_space_id=dependency.shared_space_id
			  AND head.space_generation=dependency.space_generation AND head.record_id=dependency.record_id
			  AND head.version=dependency.record_version
			JOIN topic_revision_records AS revision
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
			return nil, fmt.Errorf("%w: affected children exceed validation bound", topic.ErrContextBound)
		}
		ids = append(ids, children...)
	}
	for _, record := range records {
		ids = append(ids, record.ID)
		ids = append(ids, topic.ReferenceIDs(record)...)
		names = append(names, topic.DefinitionNames(record)...)

	}
	catalog, err := load(ids, names)
	if err != nil {
		return nil, err
	}
	requested := map[string]bool{}
	for _, id := range ids {
		requested[id] = true
	}

	for depth := 0; depth <= topic.MaxParentDepth; depth++ {
		pending := []string{}
		candidates := append([]topic.Record(nil), records...)
		for _, record := range catalog {
			candidates = append(candidates, record)
		}
		for _, record := range candidates {
			for _, id := range topic.ReferenceIDs(record) {
				if !requested[id] {
					requested[id] = true
					pending = append(pending, id)
				}
			}
		}
		if len(pending) == 0 {
			return catalog, nil
		}
		loaded, err := load(pending, nil)
		if err != nil {
			return nil, err
		}
		for id, record := range loaded {
			catalog[id] = record
		}
		if len(catalog) > maxValidationRecords {
			return nil, fmt.Errorf("%w: dependency context exceeds its bound", topic.ErrContextBound)
		}
	}
	return nil, &topic.PublicationRejection{Cause: fmt.Errorf("%w: dependency depth exceeds its bound", topic.ErrContextBound), Reason: "hierarchy_depth"}
}

func (s *Store) GetRecord(ctx context.Context, teamID, recordID string, version int64) (topic.RecordView, error) {
	if _, err := uuid.Parse(recordID); err != nil || version < 0 {
		return topic.RecordView{}, topic.ErrInvalid
	}
	var result topic.RecordView
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var body []byte
		err := tx.Raw(`SELECT revision.body FROM topic_revision_records AS revision
			JOIN topic_record_heads AS head USING (team_id,shared_space_id,space_generation,record_id)
			WHERE revision.team_id=?::uuid AND revision.shared_space_id=?::uuid AND revision.space_generation=?
			  AND revision.record_id=?::uuid AND revision.version=CASE WHEN ?=0 THEN head.version ELSE ? END`, fence.TeamID, fence.SpaceID, fence.Generation, recordID, version, version).Row().Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrNotFound
		}
		if err != nil {
			return err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return err
		}
		result, err = s.currentView(tx, fence, record)
		if err != nil || version != 0 {
			return err
		}
		views := []topic.RecordView{result}
		if err := attachCommunityPlacement(tx, fence, views); err != nil {
			return err
		}
		result = views[0]
		return nil
	})
	return result, err
}

func (s *Store) currentView(tx *gorm.DB, fence scope, record topic.Record) (topic.RecordView, error) {
	views, err := s.currentViews(tx, fence, []topic.Record{record})
	if err != nil {
		return topic.RecordView{Record: record}, err
	}
	return views[0], nil
}

func (s *Store) organizationViews(tx *gorm.DB, fence scope, records []topic.Record) ([]topic.RecordView, error) {
	var result []topic.RecordView
	for len(records) > 0 {
		count := min(topic.MaxOrganizationSources, len(records))
		for {
			views, err := s.currentViews(tx, fence, records[:count])
			// Combined closures can exceed the bound while each record remains valid.
			if errors.Is(err, topic.ErrContextBound) && count > 1 {
				count /= 2
				continue
			}
			if err != nil {
				return nil, err
			}
			result = append(result, views...)
			records = records[count:]
			break
		}
	}
	return result, nil
}

func (s *Store) currentViews(tx *gorm.DB, fence scope, records []topic.Record) ([]topic.RecordView, error) {
	if len(records) == 0 {
		return []topic.RecordView{}, nil
	}
	load := func(ids, names []string) (map[string]topic.Record, error) {
		return loadHeads(tx, fence, ids, names)
	}
	if s.recallCache != nil {
		for _, record := range records {
			s.recallCache.records[record.ID] = record
			s.recallCache.ids[record.ID] = true
		}
		// Discovery resolves name ambiguity before revision-bound hydration.
		load = func(ids, _ []string) (map[string]topic.Record, error) {
			return s.recallCache.loadHeads(tx, fence, ids)
		}
	}
	catalog, err := validationCatalogWithLoader(tx, fence, records, nil, load)
	if err != nil {
		return nil, err
	}
	handles := []topic.SourceHandle{}
	seen := map[topic.SourceHandle]bool{}
	for _, record := range catalog {
		for _, dependency := range record.Sources {
			if !seen[dependency.SourceHandle] {
				handles = append(handles, dependency.SourceHandle)
				seen[dependency.SourceHandle] = true
			}
		}
	}
	var snapshots map[topic.SourceHandle]topic.SourceSnapshot
	if s.recallCache == nil {
		snapshots = make(map[topic.SourceHandle]topic.SourceSnapshot, len(handles))
		for offset := 0; offset < len(handles); offset += topic.MaxDependencyRecords {
			batch, readErr := readSources(tx, fence, handles[offset:min(offset+topic.MaxDependencyRecords, len(handles))])
			if readErr != nil {
				return nil, readErr
			}
			for handle, snapshot := range batch {
				snapshots[handle] = snapshot
			}
		}
	} else {
		snapshots, err = s.recallCache.readSources(tx, fence, handles)
	}
	if err != nil {
		return nil, err
	}
	views := make([]topic.RecordView, 0, len(records))
	fingerprints := topic.NewRecordFingerprints(snapshots, catalog)
	for _, record := range records {
		view, err := currentRecordView(record, catalog, snapshots, fingerprints)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func currentRecordView(record topic.Record, catalog map[string]topic.Record, available map[topic.SourceHandle]topic.SourceSnapshot, fingerprints *topic.RecordFingerprints) (topic.RecordView, error) {
	view := topic.RecordView{Record: record}
	if record.Retired {
		view.StaleReason = "retired"
		return view, nil
	}
	if head, exists := catalog[record.ID]; !exists || head.Version != record.Version {
		view.StaleReason = "historical_revision"
		return view, nil
	}
	dependencies, err := topic.DependencyRecords([]topic.Record{record}, catalog)
	if err != nil {
		if errors.Is(err, topic.ErrSourceStale) {
			view.StaleReason = "dependency_changed"
			return view, nil
		}
		return view, err
	}
	sourceVersions := map[string]topic.SourceHandle{}
	for _, dependency := range dependencies {
		for _, source := range dependency.Sources {
			key := topic.SourceKey(source.SourceHandle)
			if previous, exists := sourceVersions[key]; exists && previous != source.SourceHandle {
				view.StaleReason = "dependency_changed"
				return view, nil
			}
			sourceVersions[key] = available[source.SourceHandle].SourceHandle
		}
	}
	if err := fingerprints.CheckSourceDependencies(record); err != nil {
		if errors.Is(err, topic.ErrSourceStale) {
			view.StaleReason = "source_changed"
			return view, nil
		}
		return view, err
	}
	if err := fingerprints.CheckDependencies([]topic.Record{record}); err != nil {
		if errors.Is(err, topic.ErrSourceStale) {
			view.StaleReason = "dependency_changed"
			return view, nil
		}
		return view, err
	}
	fingerprint, err := fingerprints.Fingerprint(record)
	if errors.Is(err, topic.ErrSourceStale) {
		view.StaleReason = "source_changed"
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

func (s *Store) ListRecords(ctx context.Context, teamID string, kind topic.Kind, after string, limit int) (topic.Page, error) {
	if limit < 1 || limit > topic.MaxPageSize {
		return topic.Page{}, topic.ErrInvalid
	}
	if after != "" {
		if _, err := uuid.Parse(after); err != nil {
			return topic.Page{}, topic.ErrInvalid
		}
	}
	result := topic.Page{Records: []topic.RecordView{}}
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		rows, err := tx.Raw(`SELECT revision.body FROM topic_record_heads AS head
			JOIN topic_revision_records AS revision USING (team_id,shared_space_id,space_generation,record_id,version)
			WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
			  AND (?='' OR head.kind=?) AND (?='' OR head.record_id>NULLIF(?,'')::uuid)
			ORDER BY head.record_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, string(kind), string(kind), after, after, limit+1).Rows()
		if err != nil {
			return err
		}
		var records []topic.Record
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
		if err := attachCommunityPlacement(tx, fence, result.Records); err != nil {
			return err
		}
		result.Revision, err = catalogRevision(tx, fence)
		return err
	})
	return result, err
}

func marshalRecord(record topic.Record) ([]byte, error) {
	body, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(body) > 65536 {
		return nil, fmt.Errorf("%w: record exceeds storage bound", topic.ErrInvalid)
	}
	return body, nil
}
