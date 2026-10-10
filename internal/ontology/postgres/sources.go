package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadSources(ctx context.Context, teamID string, handles []ontology.SourceHandle) ([]ontology.SourceSnapshot, error) {
	if len(handles) == 0 || len(handles) > ontology.MaxMembers {
		return nil, fmt.Errorf("%w: source batch bounds", ontology.ErrInvalid)
	}
	var result []ontology.SourceSnapshot
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

func readSource(tx *gorm.DB, fence scope, handle ontology.SourceHandle) (ontology.SourceSnapshot, error) {
	snapshots, err := readSources(tx, fence, []ontology.SourceHandle{handle})
	if err != nil {
		return ontology.SourceSnapshot{}, err
	}
	snapshot, found := snapshots[handle]
	if !found {
		snapshot = ontology.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation}
	}
	if !snapshot.Eligible || snapshot.SourceHandle != handle {
		return snapshot, ontology.ErrSourceStale
	}
	return snapshot, nil
}

func readSources(tx *gorm.DB, fence scope, handles []ontology.SourceHandle) (map[ontology.SourceHandle]ontology.SourceSnapshot, error) {
	if len(handles) > ontology.MaxDependencyRecords {
		return nil, ontology.ErrContextBound
	}
	byKind := map[ontology.SourceKind][]ontology.SourceHandle{}
	for _, handle := range handles {
		if err := ontology.ValidateSourceHandle(handle); err != nil {
			return nil, err
		}
		byKind[handle.Kind] = append(byKind[handle.Kind], handle)
	}
	result := map[ontology.SourceHandle]ontology.SourceSnapshot{}
	for _, kind := range []ontology.SourceKind{ontology.EntitySource, ontology.EvidenceSource, ontology.RelationshipSource, ontology.PredicateSource} {
		batch := byKind[kind]
		if len(batch) == 0 {
			continue
		}
		ids, versions := make([]string, len(batch)), make([]int64, len(batch))
		for i, handle := range batch {
			ids[i], versions[i] = handle.ID, handle.Version
		}
		query := map[ontology.SourceKind]string{ontology.EntitySource: entitySourceSQL, ontology.EvidenceSource: evidenceSourceSQL, ontology.RelationshipSource: relationshipSourceSQL, ontology.PredicateSource: predicateSourceSQL}[kind]
		args := []any{fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(ids)}
		if kind == ontology.PredicateSource {
			args = append(args, pq.Array(versions))
		}
		rows, err := tx.Raw(query, args...).Rows()
		if err != nil {
			return nil, fmt.Errorf("ontology: read scoped sources: %w", err)
		}
		for rows.Next() {
			snapshot := ontology.SourceSnapshot{TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation, Eligible: true}
			snapshot.Kind = kind
			var state sql.RawBytes
			if err := rows.Scan(&snapshot.ID, &snapshot.Version, &snapshot.OwnerID, &snapshot.EntityKind, &state, &snapshot.MeaningKey); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if err := json.Unmarshal(state, &snapshot.State); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("ontology: decode source state: %w", err)
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

func sourceSnapshots(tx *gorm.DB, fence scope, records []ontology.Record, catalog map[string]ontology.Record) (map[string]ontology.SourceSnapshot, error) {
	dependencies, err := ontology.DependencyRecords(records, catalog)
	if err != nil {
		return nil, err
	}
	result := map[string]ontology.SourceSnapshot{}
	for _, record := range dependencies {
		if record.Retired {
			continue
		}
		for _, dependency := range record.Sources {
			key := ontology.SourceKey(dependency.SourceHandle)
			if previous, exists := result[key]; exists {
				if previous.SourceHandle != dependency.SourceHandle {
					return nil, ontology.ErrSourceStale
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
