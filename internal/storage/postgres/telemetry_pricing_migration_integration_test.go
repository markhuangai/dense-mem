//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelemetryPricingMigrationsCreateRatesAndMarkerUniqueness(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073102)

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026073106))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026073106), "repeat migration run should remain idempotent")

	var pricingCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM app_config
		WHERE key IN (
			'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS',
			'TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS',
			'TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS'
		)
	`).Scan(&pricingCount))
	assert.Equal(t, 3, pricingCount)

	var indexDefinition string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'public'
		  AND indexname = 'placement_outcomes_telemetry_first_disposition_unique'
	`).Scan(&indexDefinition))
	assert.Contains(t, strings.ToLower(indexDefinition), "unique index")
	assert.Contains(t, indexDefinition, "(team_id, placement_run_id)")
	assert.Contains(t, indexDefinition, "outcome_kind = 'telemetry_first_disposition'")

	var originIndexDefinition string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'public'
		  AND indexname = 'knowledge_ingests_telemetry_remember_backfill_idx'
	`).Scan(&originIndexDefinition))
	assert.Contains(t, originIndexDefinition, "(team_id, ingest_id)")
	assert.Contains(t, originIndexDefinition, "_dense_mem_telemetry_origin")
	assert.Contains(t, originIndexDefinition, "contract_version")
	assert.Contains(t, originIndexDefinition, "{actor,team_id}")
	assert.Contains(t, originIndexDefinition, "{actor,profile_id}")

	var stateRLS bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT relrowsecurity AND relforcerowsecurity
		FROM pg_class
		WHERE oid = 'telemetry_first_disposition_backfill_state'::regclass
	`).Scan(&stateRLS))
	assert.True(t, stateRLS)

	var policyCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_policies
		WHERE schemaname = 'public'
		  AND tablename = 'telemetry_first_disposition_backfill_state'
		  AND policyname = 'telemetry_first_disposition_backfill_state_system_access'
	`).Scan(&policyCount))
	assert.Equal(t, 1, policyCount)
}

func TestTelemetryModelPriceMigrationPreservesExistingRates(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, migrationUpTo(ctx, sqlDB, 20260924120001))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE app_config SET value = '1.25' WHERE key = 'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS'`)
		return err
	}))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 20260927010001))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 20260927010001))
	var modelPrices, existingRate string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key = 'TELEMETRY_COST_MODEL_PRICES_JSON'`).Scan(&modelPrices))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key = 'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS'`).Scan(&existingRate))
	assert.Empty(t, modelPrices)
	assert.Equal(t, "1.25", existingRate)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE app_config SET value = $1 WHERE key = 'TELEMETRY_COST_MODEL_PRICES_JSON'`,
			`[{"component":"verifier","model":"alternate","input_usd_per_million_tokens":1,"output_usd_per_million_tokens":2}]`)
		return err
	}))
	require.NoError(t, migrationDownTo(ctx, sqlDB, 20260924120001))
	var modelPriceKeys int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM app_config WHERE key = 'TELEMETRY_COST_MODEL_PRICES_JSON'`).Scan(&modelPriceKeys))
	require.Zero(t, modelPriceKeys)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key = 'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS'`).Scan(&existingRate))
	assert.Equal(t, "1.25", existingRate)
}

func TestUsageCredentialBucketMigrationUpAndDown(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, migrationUpTo(ctx, sqlDB, 20260927010001))
	require.False(t, tableExists(t, ctx, sqlDB, "usage_credential_buckets"))

	require.NoError(t, migrationUpTo(ctx, sqlDB, 20260927010002))
	require.True(t, tableExists(t, ctx, sqlDB, "usage_credential_buckets"))
	var rlsEnabled bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT relrowsecurity AND relforcerowsecurity
		FROM pg_class WHERE oid = 'usage_credential_buckets'::regclass
	`).Scan(&rlsEnabled))
	require.True(t, rlsEnabled)
	var policyCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_policies
		WHERE schemaname = 'public' AND tablename = 'usage_credential_buckets'
	`).Scan(&policyCount))
	require.Equal(t, 2, policyCount)

	teamID, credentialID := uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1, $2)`, teamID, "usage-migration-"+teamID.String()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_credential_buckets (bucket_start, team_id, credential_id, route, method, status_class, request_count)
			VALUES (now(), $1, $2, '/mcp', 'POST', 2, 1)
		`, teamID, credentialID)
		return err
	}))
	var rows int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_credential_buckets`).Scan(&rows))
	require.Equal(t, 1, rows)

	require.NoError(t, migrationDownTo(ctx, sqlDB, 20260927010001))
	require.False(t, tableExists(t, ctx, sqlDB, "usage_credential_buckets"))
	require.True(t, tableExists(t, ctx, sqlDB, "usage_metric_buckets"))
}

func TestTelemetryFirstDispositionMigrationRebuildsInvalidConcurrentIndex(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073105)
	insertDuplicateTelemetryFirstDispositionMarkers(t, ctx, sqlDB)

	require.Error(t, migrationUpTo(ctx, sqlDB, 2026073106), "duplicate marker rows must fail the first unique-index build")

	var indexValid bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT index_meta.indisvalid
		FROM pg_index AS index_meta
		JOIN pg_class AS index_class ON index_class.oid = index_meta.indexrelid
		WHERE index_class.relname = 'placement_outcomes_telemetry_first_disposition_unique'
	`).Scan(&indexValid))
	assert.False(t, indexValid)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `TRUNCATE TABLE placement_outcomes`)
		return err
	}))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026073106), "retry must replace the invalid index before rebuilding it")

	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT index_meta.indisvalid
		FROM pg_index AS index_meta
		JOIN pg_class AS index_class ON index_class.oid = index_meta.indexrelid
		WHERE index_class.relname = 'placement_outcomes_telemetry_first_disposition_unique'
	`).Scan(&indexValid))
	assert.True(t, indexValid)
	assert.False(t, indexExists(t, ctx, sqlDB, "placement_outcomes_telemetry_first_disposition_invalid"))
}

func TestTelemetryFirstDispositionMigrationPreservesValidConcurrentIndex(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073105)
	_, err := sqlDB.ExecContext(ctx, `
		CREATE UNIQUE INDEX CONCURRENTLY placement_outcomes_telemetry_first_disposition_unique
		ON placement_outcomes(team_id, placement_run_id)
		WHERE outcome_kind = 'telemetry_first_disposition'
	`)
	require.NoError(t, err)

	beforeOID := telemetryFirstDispositionIndexOID(t, ctx, sqlDB)
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026073106))

	assert.Equal(t, beforeOID, telemetryFirstDispositionIndexOID(t, ctx, sqlDB))
}

func TestTeamOwnedDreamingRepairHandlesLegacyTelemetryVersionSkip(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073101)
	assert.True(t, columnExists(t, ctx, sqlDB, "hypotheses", "owner_profile_id"))
	assert.True(t, columnExists(t, ctx, sqlDB, "dream_cycle_runs", "owner_profile_id"))

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app_config (key, value)
			VALUES
				('TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS', '3.50'),
				('TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS', ''),
				('TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS', '')
			ON CONFLICT (key) DO NOTHING
		`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO goose_db_version (version_id, is_applied, tstamp)
			VALUES (2026073102, true, now())
		`)
		return err
	}))

	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunUp(ctx))

	assert.False(t, columnExists(t, ctx, sqlDB, "hypotheses", "owner_profile_id"))
	assert.True(t, columnExists(t, ctx, sqlDB, "hypotheses", "created_by_profile_id"))
	assert.False(t, columnExists(t, ctx, sqlDB, "dream_cycle_runs", "owner_profile_id"))
	assert.True(t, columnExists(t, ctx, sqlDB, "dream_cycle_runs", "initiated_by_profile_id"))
	assert.True(t, tableExists(t, ctx, sqlDB, "hypothesis_feedback_events"))

	var pricingCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM app_config
		WHERE key IN (
			'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS',
			'TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS',
			'TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS'
		)
	`).Scan(&pricingCount))
	assert.Equal(t, 3, pricingCount)

	var verifierInputRate string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT value
		FROM app_config
		WHERE key = 'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS'
	`).Scan(&verifierInputRate))
	assert.Equal(t, "3.50", verifierInputRate, "the repair path must not overwrite operator pricing")
}

func insertDuplicateTelemetryFirstDispositionMarkers(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
		`, teamID, ownerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'completed')
		`, teamID, ingestID, ownerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id, status, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'completed', clock_timestamp())
		`, teamID, runID, ingestID, ownerID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_outcomes (
				team_id, placement_run_id, owner_profile_id,
				outcome_kind, status, idempotency_key, payload
			) VALUES
				($1::uuid, $2::uuid, $3::uuid, 'telemetry_first_disposition', 'completed', '', '{}'::jsonb),
				($1::uuid, $2::uuid, $3::uuid, 'telemetry_first_disposition', 'completed', '', '{}'::jsonb)
		`, teamID, runID, ownerID)
		return err
	}))
}

func telemetryFirstDispositionIndexOID(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var oid string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT oid::text
		FROM pg_class
		WHERE relname = 'placement_outcomes_telemetry_first_disposition_unique'
	`).Scan(&oid))
	return oid
}
