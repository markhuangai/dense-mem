package postgres

import (
	"context"
	"encoding/json"

	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"gorm.io/gorm"
)

func (r *Store) RecordSessionDiagnostic(ctx context.Context, scope session.Scope, id string, body json.RawMessage) error {
	if !json.Valid(body) {
		return session.ErrInvalidInput
	}
	return r.withTeamProfileTx(ctx, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
		if err := sessionSpaceFence(ctx, tx, scope); err != nil {
			return err
		}
		return tx.WithContext(ctx).Exec(`INSERT INTO session_submission_diagnostics
		 (team_id, owner_profile_id, space_id, space_generation, submission_id, body)
		 VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?::uuid, ?::jsonb)`,
			scope.TeamID, scope.OwnerProfileID, scope.SpaceID, scope.SpaceGeneration, id, string(body)).Error
	})
}
