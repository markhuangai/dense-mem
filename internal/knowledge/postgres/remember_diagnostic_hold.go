package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/lib/pq"

	"github.com/google/uuid"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func (r *Store) synchronizeRememberAttemptDiagnosticHold(ctx context.Context, rawSpaceID string) error {
	spaceID, err := uuid.Parse(rawSpaceID)
	if err != nil {
		return fmt.Errorf("space_id is invalid: %w", err)
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var lockedSpace uuid.UUID
		if err := tx.WithContext(ctx).Raw(`
			SELECT id
			FROM memory_spaces
			WHERE id = $1 AND kind = 'private'
			FOR UPDATE
		`, spaceID).Row().Scan(&lockedSpace); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		var held bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT EXISTS (
				SELECT 1 FROM private_memory_legal_holds
				WHERE space_id = $1 AND released_at IS NULL
			)
			`, lockedSpace).Row().Scan(&held); err != nil {
			return err
		}
		return storagepostgres.SetRememberAttemptDiagnosticHoldStateTx(ctx, tx, lockedSpace, held)
	})
}

func (r *Store) purgeExpiredPrivateDiagnostics(ctx context.Context, table string, batchSize int) (int, error) {
	idColumn := "diagnostic_id"
	switch table {
	case "remember_attempt_diagnostics", "session_submission_diagnostics":
	case "remember_invocation_diagnostics":
		idColumn = "invocation_id"
	case "remember_failure_artifacts":
		idColumn = "artifact_id"
	default:
		return 0, fmt.Errorf("unknown diagnostic retention table %q", table)
	}
	var deleted int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var spaces pq.StringArray
		if err := tx.Raw(`SELECT COALESCE(array_agg(space_id::text),ARRAY[]::text[]) FROM (
    SELECT DISTINCT space_id FROM `+pq.QuoteIdentifier(table)+` WHERE space_id IS NOT NULL AND expires_at<=clock_timestamp()
     AND NOT dense_mem_private_space_held(space_id) ORDER BY space_id LIMIT ?
  ) candidates`, batchSize).Row().Scan(&spaces); err != nil {
			return err
		}
		// A candidate is safe only when every space that can hold its lineage is locked.
		if err := tx.Raw(`WITH lineage AS MATERIALIZED (
    SELECT source.id, related.space_id FROM unnest(?::uuid[]) source(id)
    CROSS JOIN LATERAL dense_mem_private_lineage_spaces(source.id) related
  ), locked AS MATERIALIZED (
    SELECT id FROM memory_spaces WHERE id IN (SELECT space_id FROM lineage)
    ORDER BY id FOR KEY SHARE SKIP LOCKED
  ) SELECT COALESCE(array_agg(source.id::text),ARRAY[]::text[]) FROM unnest(?::uuid[]) source(id)
    WHERE NOT EXISTS (SELECT 1 FROM lineage WHERE lineage.id=source.id
      AND NOT EXISTS (SELECT 1 FROM locked WHERE locked.id=lineage.space_id))`, pq.Array(spaces), pq.Array(spaces)).Row().Scan(&spaces); err != nil {
			return err
		}
		if err := tx.Exec(`SELECT set_config('app.remember_attempt_diagnostic_purge','true',true),set_config('app.remember_failure_artifact_purge','true',true),set_config('app.session_diagnostic_purge','true',true)`).Error; err != nil {
			return err
		}
		result := tx.Exec(`WITH candidates AS (
    SELECT team_id,space_id,`+pq.QuoteIdentifier(idColumn)+` AS id FROM `+pq.QuoteIdentifier(table)+`
    WHERE (space_id IS NULL OR space_id=ANY(?::uuid[])) AND expires_at<=clock_timestamp() AND NOT dense_mem_private_space_held(space_id)
    ORDER BY expires_at,team_id,`+pq.QuoteIdentifier(idColumn)+` LIMIT ?
  ) DELETE FROM `+pq.QuoteIdentifier(table)+` diagnostic USING candidates
   WHERE diagnostic.team_id=candidates.team_id AND diagnostic.space_id IS NOT DISTINCT FROM candidates.space_id AND diagnostic.`+pq.QuoteIdentifier(idColumn)+`=candidates.id`, pq.Array(spaces), batchSize)
		deleted = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return 0, fmt.Errorf("purge %s: %w", table, err)
	}
	return int(deleted), nil
}
