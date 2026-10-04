package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
	if err := ontology.ValidateSourceHandle(handle); err != nil {
		return ontology.SourceSnapshot{}, err
	}
	snapshot := ontology.SourceSnapshot{SourceHandle: handle, TeamID: fence.TeamID, SpaceID: fence.SpaceID, Generation: fence.Generation}
	var query string
	args := []any{fence.TeamID, fence.SpaceID, fence.Generation, handle.ID}
	switch handle.Kind {
	case ontology.EntitySource:
		query = entitySourceSQL
	case ontology.EvidenceSource:
		query = evidenceSourceSQL
	case ontology.RelationshipSource:
		query = relationshipSourceSQL
	case ontology.PredicateSource:
		query = predicateSourceSQL
		args = append(args, handle.Version)
	}
	var version int64
	var state []byte
	err := tx.Raw(query, args...).Row().Scan(&version, &snapshot.OwnerID, &snapshot.EntityKind, &state, &snapshot.MeaningKey)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, ontology.ErrSourceStale
	}
	if err != nil {
		return snapshot, fmt.Errorf("ontology: read scoped source: %w", err)
	}
	if version != handle.Version {
		return snapshot, ontology.ErrSourceStale
	}
	if err := json.Unmarshal(state, &snapshot.State); err != nil {
		return snapshot, fmt.Errorf("ontology: decode source state: %w", err)
	}
	snapshot.Eligible = true
	return snapshot, nil
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
