package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) History(ctx context.Context, teamID string, after int64, limit int) ([]topic.PublicationHistory, error) {
	if after < 0 || limit < 1 || limit > topic.MaxPageSize {
		return nil, topic.ErrInvalid
	}
	result := []topic.PublicationHistory{}
	err := s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		rows, err := tx.Raw(`SELECT result,origin,COALESCE(actor_id::text,''),reason,created_at
			FROM topic_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
			AND revision>? ORDER BY revision LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation, after, limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item topic.PublicationHistory
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

func (s *Store) Rollback(ctx context.Context, teamID, targetID, key string, expected int64, reason string) (result topic.PublicationResult, err error) {
	actor, err := managerActor(ctx, teamID)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	if _, err := uuid.Parse(targetID); err != nil {
		return topic.PublicationResult{}, topic.ErrInvalid
	}
	if err := topic.ValidateOperation(key, expected, reason); err != nil {
		return topic.PublicationResult{}, err
	}
	input := topic.Publication{OperationKey: key, ExpectedRevision: expected, Reason: reason, RollbackOf: targetID}
	ctx, diagnostic, finish := topic.ObservePublication(ctx, s.publicationObserver, teamID, "rollback", input)
	defer func() {
		if diagnostic != nil && result.ID != "" {
			diagnostic.Result = &result
		}
		finish(err)
	}()
	hash, err := topic.RequestHash(input, "rollback", actor)
	if err != nil {
		return topic.PublicationResult{}, err
	}
	var replay topic.PublicationResult
	var found bool
	err = s.withScope(ctx, teamID, true, func(tx *gorm.DB, fence scope) error {
		var err error
		replay, found, err = replayPublication(tx, fence, key, hash)
		if err != nil || found {
			return err
		}
		input, err = prepareTopicRollback(tx, fence, input)
		return err
	})
	if err != nil {
		return topic.PublicationResult{}, err
	}
	if found {
		return replay, nil
	}
	return s.commit(ctx, teamID, input, "rollback", actor, hash)
}

func prepareTopicRollback(tx *gorm.DB, fence scope, input topic.Publication) (topic.Publication, error) {
	var encoded []byte
	err := tx.Raw(`SELECT result FROM topic_publications WHERE team_id=?::uuid AND shared_space_id=?::uuid
		AND space_generation=? AND publication_id=?::uuid`, fence.TeamID, fence.SpaceID, fence.Generation, input.RollbackOf).Row().Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return input, topic.ErrNotFound
	}
	if err != nil {
		return input, err
	}
	var target topic.PublicationResult
	if err := json.Unmarshal(encoded, &target); err != nil {
		return input, err
	}
	for _, ref := range target.Records {
		current, err := loadHeads(tx, fence, []string{ref.ID}, nil)
		if err != nil {
			return input, err
		}
		head, exists := current[ref.ID]
		if !exists || head.Version != ref.Version {
			return input, topic.PublicationVersionRejection("rollback_revision", ref.ID, ref.Version, head.Version)
		}
		desired := head
		if ref.Version == 1 {
			desired.Retired = true
		} else {
			var body []byte
			if err := tx.Raw(`SELECT body FROM topic_revision_records WHERE team_id=?::uuid
				AND shared_space_id=?::uuid AND space_generation=? AND record_id=?::uuid AND version=?`,
				fence.TeamID, fence.SpaceID, fence.Generation, ref.ID, ref.Version-1).Row().Scan(&body); err != nil {
				return input, err
			}
			desired, err = decodeRecord(body)
			if err != nil {
				return input, err
			}
		}
		input.Changes = append(input.Changes, topic.Change{ExpectedVersion: head.Version, Record: desired})
	}
	records := make([]topic.Record, 0, len(input.Changes))
	for _, change := range input.Changes {
		records = append(records, change.Record)
	}
	catalog, err := validationCatalog(tx, fence, records, nil)
	if err != nil {
		return input, err
	}
	for i := range input.Changes {
		if input.Changes[i].Record.Retired {
			continue
		}
		for j := range input.Changes[i].Record.Dependencies {
			if dependency, exists := catalog[input.Changes[i].Record.Dependencies[j].ID]; exists {
				input.Changes[i].Record.Dependencies[j].Version = dependency.Version
			}
			for _, change := range input.Changes {
				if input.Changes[i].Record.Dependencies[j].ID == change.Record.ID {
					input.Changes[i].Record.Dependencies[j].Version = change.ExpectedVersion + 1
				}
			}
		}
	}
	return input, topic.ValidatePublication(input)
}
