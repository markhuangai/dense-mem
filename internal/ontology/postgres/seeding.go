package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) SeedDefinitions(ctx context.Context, teamID string, input ontology.SeedInput) (ontology.SeedResult, error) {
	if err := requireAutomatic(ctx); err != nil {
		return ontology.SeedResult{}, err
	}
	if input.Limit < 1 || input.Limit > 20 || len(input.AfterPredicate) > 128 || strings.ContainsRune(input.AfterPredicate, 0) {
		return ontology.SeedResult{}, ontology.ErrInvalid
	}
	if err := ontology.ValidateOperation(input.OperationKey, input.ExpectedRevision, "seed ontology definitions"); err != nil {
		return ontology.SeedResult{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return ontology.SeedResult{}, err
	}
	logical := ontology.Publication{OperationKey: input.OperationKey, ExpectedRevision: input.ExpectedRevision, Reason: string(encoded)}
	hash, err := ontology.RequestHash(logical, "seed", "")
	if err != nil {
		return ontology.SeedResult{}, err
	}
	publication := ontology.Publication{OperationKey: input.OperationKey, ExpectedRevision: input.ExpectedRevision, Reason: "seed ontology definitions"}
	var replay ontology.PublicationResult
	var found bool
	var next string
	err = s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var err error
		replay, found, err = replayPublication(tx, fence, input.OperationKey, hash)
		if err != nil || found {
			return err
		}
		rows, err := tx.Raw(eligiblePredicateSelect+` AND definition.predicate_key>? ORDER BY definition.predicate_key,definition.version DESC LIMIT ?`,
			fence.TeamID, fence.SpaceID, fence.Generation, input.AfterPredicate, input.Limit+1).Rows()
		if err != nil {
			return err
		}
		var handles []ontology.SourceHandle
		for rows.Next() {
			handle := ontology.SourceHandle{Kind: ontology.PredicateSource}
			if err := rows.Scan(&handle.ID, &handle.Version); err != nil {
				_ = rows.Close()
				return err
			}
			handles = append(handles, handle)
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if len(handles) > input.Limit {
			next = handles[input.Limit-1].ID
			handles = handles[:input.Limit]
		}
		var snapshots []ontology.SourceSnapshot
		for _, handle := range handles {
			snapshot, err := readSource(tx, fence, handle)
			if err != nil {
				return err
			}
			snapshots = append(snapshots, snapshot)
		}
		candidates, err := ontology.SeedRecords(teamID, snapshots)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(candidates))
		for _, record := range candidates {
			ids = append(ids, record.ID)
		}
		current, err := loadHeads(tx, fence, ids, nil, nil)
		if err != nil {
			return err
		}
		for _, record := range candidates {
			if _, exists := current[record.ID]; !exists {
				publication.Changes = append(publication.Changes, ontology.Change{Record: record})
			}
		}
		return nil
	})
	if err != nil {
		return ontology.SeedResult{}, fmt.Errorf("ontology: seed: %w", err)
	}
	if found {
		return ontology.SeedResult{PublicationResult: replay}, nil
	}
	result, err := s.commit(ctx, teamID, publication, "seed", "", hash, next)
	return ontology.SeedResult{PublicationResult: result}, err
}
