//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestSubmissionAssessmentMigrationBlocksAwaitingReviewRuns(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080501)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	ingestID := uuid.NewString()
	placementRunID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
		`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'queued')
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
			    team_id, placement_run_id, ingest_id, owner_profile_id, status, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'awaiting_review', now())
		`, teamID, placementRunID, ingestID, profileID)
		return err
	}))

	require.NoError(t, goose.SetDialect("postgres"))
	err := goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080502)
	require.ErrorContains(t, err, "1 unfinished placement runs")
}
