package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) History(ctx context.Context, teamID string, after int64, limit int) ([]ontology.PublicationHistory, error) {
	if after < 0 || limit < 1 || limit > ontology.MaxPageSize {
		return nil, ontology.ErrInvalid
	}
	result := []ontology.PublicationHistory{}
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		rows, err := tx.Raw(`SELECT result,origin,COALESCE(actor_id::text,''),reason,created_at
			FROM ontology_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
			AND revision>? ORDER BY revision LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, after, limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ontology.PublicationHistory
			var encoded []byte
			if err := rows.Scan(&encoded, &item.Origin, &item.ActorID, &item.Reason, &item.CreatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &item.PublicationResult); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func (s *Store) Rollback(ctx context.Context, teamID, targetID, key string, expected int64, reason string) (ontology.PublicationResult, error) {
	actor, err := managerActor(ctx, teamID)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	if _, err := uuid.Parse(targetID); err != nil {
		return ontology.PublicationResult{}, ontology.ErrInvalid
	}
	if err := ontology.ValidateOperation(key, expected, reason); err != nil {
		return ontology.PublicationResult{}, err
	}
	input := ontology.Publication{OperationKey: key, ExpectedRevision: expected, Reason: reason, RollbackOf: targetID}
	hash, err := ontology.RequestHash(input, "rollback", actor)
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	var replay ontology.PublicationResult
	var found bool
	err = s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var err error
		replay, found, err = replayPublication(tx, fence, key, hash)
		if err != nil || found {
			return err
		}
		var encoded []byte
		err = tx.Raw(`SELECT result FROM ontology_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid
			AND space_generation=? AND publication_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, targetID).Row().Scan(&encoded)
		if errors.Is(err, sql.ErrNoRows) {
			return ontology.ErrNotFound
		}
		if err != nil {
			return err
		}
		var target ontology.PublicationResult
		if err := json.Unmarshal(encoded, &target); err != nil {
			return err
		}
		for _, ref := range target.Records {
			current, err := loadHeads(tx, fence, []string{ref.ID}, nil, nil)
			if err != nil {
				return err
			}
			head, exists := current[ref.ID]
			if !exists || head.Version != ref.Version {
				return ontology.ErrConflict
			}
			desired := head
			if ref.Version == 1 {
				desired.Retired = true
			} else {
				var body []byte
				if err := tx.Raw(`SELECT body FROM ontology_record_revisions WHERE team_id=?::uuid
					AND shared_space_id=?::uuid AND space_generation=? AND record_id=?::uuid AND version=?`,
					fence.TeamID, fence.SpaceID, fence.Generation, ref.ID, ref.Version-1).Row().Scan(&body); err != nil {
					return err
				}
				desired, err = decodeRecord(body)
				if err != nil {
					return err
				}
			}
			input.Changes = append(input.Changes, ontology.Change{ExpectedVersion: head.Version, Record: desired})
		}
		for i := range input.Changes {
			if input.Changes[i].Record.Retired {
				continue
			}
			for j := range input.Changes[i].Record.Dependencies {
				for _, change := range input.Changes {
					if input.Changes[i].Record.Dependencies[j].ID == change.Record.ID {
						input.Changes[i].Record.Dependencies[j].Version = change.ExpectedVersion + 1
					}
				}
			}
		}
		return ontology.ValidatePublication(input)
	})
	if err != nil {
		return ontology.PublicationResult{}, err
	}
	if found {
		return replay, nil
	}
	return s.commit(ctx, teamID, input, "rollback", actor, hash, "")
}
