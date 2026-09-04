//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	hourlyEvidenceDreamsMigrationBase    int64 = 20260903010001
	hourlyEvidenceDreamsMigrationVersion int64 = 20260904030001
)

func TestHourlyEvidenceDreamsMigrationDownRejectsEvidenceCycleHistory(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, hourlyEvidenceDreamsMigrationBase)
	runGooseUpTo(t, ctx, db, hourlyEvidenceDreamsMigrationVersion)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, db)
	var spaceID string
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO dream_cycle_runs (
				team_id, space_id, space_generation, initiated_by_profile_id,
				run_date, window_key, lane, status, source_snapshot
			) VALUES ($1::uuid, $2::uuid, $3, $4::uuid, '2026-09-04',
			          'hour:2026-09-04T03', 'evidence_discovery', 'completed', '[]'::jsonb)
		`, teamID, spaceID, generation, profileID)
		return err
	}))

	require.Error(t, migrationDownTo(ctx, db, hourlyEvidenceDreamsMigrationBase))
	require.True(t, tableExists(t, ctx, db, "dream_evidence_target_attempts"))
	require.True(t, tableExists(t, ctx, db, "dream_evidence_target_evaluations"))
	require.True(t, tableExists(t, ctx, db, "hypothesis_evidence_derivation_sources"))
}

func TestHourlyEvidenceDreamsMigrationPreservesGraphClaimArbiter(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, hourlyEvidenceDreamsMigrationBase)
	runGooseUpTo(t, ctx, db, hourlyEvidenceDreamsMigrationVersion)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, db)
	var spaceID string
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		const claim = `
			INSERT INTO dream_cycle_runs (
				team_id, space_id, space_generation, initiated_by_profile_id,
				run_date, window_key, status, source_snapshot
			) VALUES ($1::uuid, $2::uuid, $3, $4::uuid, '2026-09-04',
			          '2026-09-04', 'running', '[]'::jsonb)
			ON CONFLICT (team_id, window_key)
			WHERE canonical_run_id IS NULL
			DO NOTHING
			RETURNING run_id::text`
		var runID string
		if err := tx.QueryRowContext(ctx, claim, teamID, spaceID, generation, profileID).Scan(&runID); err != nil {
			return err
		}
		if runID == "" {
			return fmt.Errorf("legacy graph claim returned an empty run id")
		}
		var duplicate string
		err := tx.QueryRowContext(ctx, claim, teamID, spaceID, generation, profileID).Scan(&duplicate)
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("legacy graph claim duplicate result: %w", err)
		}
		return nil
	}))
}
