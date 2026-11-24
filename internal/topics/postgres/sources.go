package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadSources(ctx context.Context, teamID string, handles []topic.SourceHandle) ([]topic.SourceSnapshot, error) {
	if len(handles) == 0 || len(handles) > topic.MaxMembers {
		return nil, fmt.Errorf("%w: source batch bounds", topic.ErrInvalid)
	}
	var result []topic.SourceSnapshot
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		for _, handle := range handles {
			snapshot, err := readSource(tx, fence, handle)
			if err != nil {
				return err
			}
			result = append(result, snapshot)
		}
		return nil
	})
	return result, err
}

func readSource(tx *gorm.DB, fence scope, handle topic.SourceHandle) (topic.SourceSnapshot, error) {
	snapshots, err := readSources(tx, fence, []topic.SourceHandle{handle})
	if err != nil {
		return topic.SourceSnapshot{}, err
	}
	snapshot, found := snapshots[handle]
	if !found {
		snapshot = topic.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation}
	}
	if !snapshot.Eligible || snapshot.SourceHandle != handle {
		return snapshot, topic.ErrSourceStale
	}
	return snapshot, nil
}

func readSources(tx *gorm.DB, fence scope, handles []topic.SourceHandle) (map[topic.SourceHandle]topic.SourceSnapshot, error) {
	if len(handles) > topic.MaxDependencyRecords {
		return nil, topic.ErrContextBound
	}
	byKind := map[topic.SourceKind][]topic.SourceHandle{}
	for _, handle := range handles {
		if err := topic.ValidateSourceHandle(handle); err != nil {
			return nil, err
		}
		byKind[handle.Kind] = append(byKind[handle.Kind], handle)
	}
	result := map[topic.SourceHandle]topic.SourceSnapshot{}
	for _, kind := range []topic.SourceKind{topic.EvidenceSource} {
		batch := byKind[kind]
		if len(batch) == 0 {
			continue
		}
		ids := make([]string, len(batch))
		for i, handle := range batch {
			ids[i] = handle.ID
		}
		query := evidenceSourceSQL
		args := []any{fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)}
		rows, err := tx.Raw(query, args...).Rows()
		if err != nil {
			return nil, fmt.Errorf("topics: read scoped sources: %w", err)
		}
		for rows.Next() {
			snapshot := topic.SourceSnapshot{TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, Eligible: true}
			snapshot.Kind = kind
			var state sql.RawBytes
			if err := rows.Scan(&snapshot.ID, &snapshot.Version, &snapshot.OwnerID, &state, &snapshot.MeaningKey); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if err := json.Unmarshal(state, &snapshot.State); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("topics: decode source state: %w", err)
			}
			result[snapshot.SourceHandle] = snapshot
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func sourceSnapshots(tx *gorm.DB, fence scope, records []topic.Record, catalog map[string]topic.Record) (map[string]topic.SourceSnapshot, error) {
	dependencies, err := topic.DependencyRecords(records, catalog)
	if err != nil {
		return nil, err
	}
	result := map[string]topic.SourceSnapshot{}
	for _, record := range dependencies {
		if record.Retired {
			continue
		}
		for _, dependency := range record.Sources {
			key := topic.SourceKey(dependency.SourceHandle)
			if previous, exists := result[key]; exists {
				if previous.SourceHandle != dependency.SourceHandle {
					return nil, topic.ErrSourceStale
				}
				continue
			}
			snapshot, err := readSource(tx, fence, dependency.SourceHandle)
			if err != nil {
				return nil, err
			}
			result[key] = snapshot
		}
	}
	return result, nil
}
