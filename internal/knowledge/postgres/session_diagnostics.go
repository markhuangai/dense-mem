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

func (r *Store) purgeExpiredSessionDiagnostics(ctx context.Context) (int, error) {
	var deleted int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.session_diagnostic_purge', 'true', true)").Error; err != nil {
			return err
		}
		var ids []string
		if err := tx.WithContext(ctx).Raw(`SELECT diagnostic.diagnostic_id::text
          FROM session_submission_diagnostics AS diagnostic
          JOIN memory_spaces AS space ON space.team_id = diagnostic.team_id AND space.id = diagnostic.space_id
          WHERE diagnostic.expires_at <= clock_timestamp()
          AND NOT EXISTS (SELECT 1 FROM private_memory_legal_holds AS hold WHERE hold.space_id = diagnostic.space_id AND hold.released_at IS NULL)
          ORDER BY diagnostic.expires_at, diagnostic.diagnostic_id LIMIT 100 FOR KEY SHARE OF space`).Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		result := tx.WithContext(ctx).Exec(`DELETE FROM session_submission_diagnostics
          WHERE diagnostic_id IN ? AND expires_at <= clock_timestamp()
          AND NOT EXISTS (SELECT 1 FROM private_memory_legal_holds AS hold WHERE hold.space_id = session_submission_diagnostics.space_id AND hold.released_at IS NULL)`, ids)
		deleted = result.RowsAffected
		return result.Error
	})
	return int(deleted), err
}
