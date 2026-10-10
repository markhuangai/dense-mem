package postgres

import (
	"context"
	"encoding/json"
	"errors"

	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"gorm.io/gorm"
)

func (r *Store) SaveSessionExtraction(ctx context.Context, scope session.Scope, id string, index int, body json.RawMessage) error {
	if index < 0 || index >= session.MaxWindows {
		return session.ErrInvalidInput
	}
	return r.saveSessionCheckpoint(ctx, scope, id, index, body)
}

func (r *Store) SaveSessionLinking(ctx context.Context, scope session.Scope, id string, body json.RawMessage) error {
	return r.saveSessionCheckpoint(ctx, scope, id, -1, body)
}

func (r *Store) saveSessionCheckpoint(ctx context.Context, scope session.Scope, id string, index int, body json.RawMessage) error {
	if !json.Valid(body) {
		return session.ErrInvalidInput
	}
	return r.withTeamProfileTx(ctx, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
		if err := sessionSpaceFence(ctx, tx, scope); err != nil {
			return err
		}
		created := tx.WithContext(ctx).Exec(`
			INSERT INTO session_extraction_checkpoints (team_id, owner_profile_id, space_id, space_generation, submission_id, window_index, body)
			SELECT ?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?, ?::jsonb
			WHERE EXISTS (SELECT 1 FROM session_submissions WHERE team_id = ?::uuid AND space_id = ?::uuid
			 AND owner_profile_id = ?::uuid AND submission_id = ?::uuid AND space_generation = ?
			 AND (result IS NULL OR result->>'processing_state' <> 'completed'))
			ON CONFLICT (team_id, space_id, submission_id, window_index) DO NOTHING
		`, scope.TeamID, scope.OwnerProfileID, scope.SpaceID, scope.SpaceGeneration, id, index, string(body),
			scope.TeamID, scope.SpaceID, scope.OwnerProfileID, id, scope.SpaceGeneration)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 1 {
			return nil
		}
		var same bool
		err := tx.WithContext(ctx).Raw(`SELECT body = ?::jsonb FROM session_extraction_checkpoints
		 WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
		 AND submission_id = ?::uuid AND window_index = ? AND space_generation = ?`,
			string(body), scope.TeamID, scope.SpaceID, scope.OwnerProfileID, id, index, scope.SpaceGeneration).Row().Scan(&same)
		if err != nil {
			return err
		}
		if !same {
			return session.ErrRequestConflict
		}
		return nil
	})
}

func insertSessionReceipt(ctx context.Context, tx *gorm.DB, scope session.Scope, id string, result session.Result) error {
	if result.SubmissionID != id {
		return session.ErrInvalidInput
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	updated := tx.WithContext(ctx).Exec(`UPDATE session_submissions SET result = ?::jsonb
	 WHERE team_id = ?::uuid AND space_id = ?::uuid AND owner_profile_id = ?::uuid
	 AND submission_id = ?::uuid AND space_generation = ?
	 AND (result IS NULL OR result->>'processing_state' <> 'completed')`,
		string(body), scope.TeamID, scope.SpaceID, scope.OwnerProfileID, id, scope.SpaceGeneration)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return session.ErrStale
	}
	return tx.WithContext(ctx).Exec(`INSERT INTO session_submission_receipts
	 (team_id, owner_profile_id, space_id, space_generation, submission_id, body)
	 VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?::jsonb)`,
		scope.TeamID, scope.OwnerProfileID, scope.SpaceID, scope.SpaceGeneration, id, string(body)).Error
}

func (r *Store) RecordSessionFailure(ctx context.Context, scope session.Scope, id string, result session.Result) error {
	if result.ProcessingState != "failed" {
		return errors.New("session: failure receipt must report failed processing")
	}
	return r.withTeamProfileTx(ctx, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
		if err := sessionSpaceFence(ctx, tx, scope); err != nil {
			return err
		}
		return insertSessionReceipt(ctx, tx, scope, id, result)
	})
}
