//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRememberRetryActivationMigrationUsesRetryableFailurePredicate(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	runGooseUpTo(t, ctx, db, 20260901020001)

	var lockTimeout string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&lockTimeout))
	require.Equal(t, "0", lockTimeout, "the migration must not leak its session lock timeout into the application pool")

	var predicate string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT pg_get_expr(indexes.indpred, indexes.indrelid)
		FROM pg_index AS indexes
		WHERE indexes.indexrelid = 'remember_attempts_failed_retryable_idx'::regclass
	`).Scan(&predicate))
	require.Contains(t, strings.ToLower(predicate), "outcome = 'failed'")
	require.Contains(t, strings.ToLower(predicate), "coalesce(retryable, true)")

	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	rows := []struct {
		attemptID string
		key       string
		retryable *bool
		outcome   string
	}{
		{attemptID: uuid.NewString(), key: "retry-null", outcome: "failed"},
		{attemptID: uuid.NewString(), key: "retry-false", retryable: boolPointer(false), outcome: "failed"},
		{attemptID: uuid.NewString(), key: "retry-true", retryable: boolPointer(true), outcome: "failed"},
		{attemptID: uuid.NewString(), key: "retry-completed", retryable: boolPointer(false), outcome: "completed"},
	}
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, row := range rows {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO remember_attempts (
				    team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				    contract_version, submission_kind, outcome, retryable, public_result, completed_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 'remember', $7, $8, '{}'::jsonb, now())
			`, teamID, row.attemptID, profileID, row.key, row.key+"-hash", domain.ContractVersion, row.outcome, row.retryable); err != nil {
				return err
			}
		}
		return nil
	}))

	var retryableCount, nonRetryableCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM remember_attempts
		WHERE team_id = $1::uuid AND outcome = 'failed' AND COALESCE(retryable, true)
	`, teamID).Scan(&retryableCount))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM remember_attempts
		WHERE team_id = $1::uuid AND outcome = 'failed' AND NOT COALESCE(retryable, true)
	`, teamID).Scan(&nonRetryableCount))
	require.Equal(t, 2, retryableCount)
	require.Equal(t, 1, nonRetryableCount)

	// The forward-only migration is safe to rerun after the index is valid.
	require.NoError(t, migrationUpTo(ctx, db, 20260901020001))
}

func boolPointer(value bool) *bool {
	return &value
}
