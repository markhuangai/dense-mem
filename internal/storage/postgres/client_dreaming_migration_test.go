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

func TestTeamOwnedDreamingMigrationCanonicalizesLegacyRows(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073002)
	teamID, firstProfileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	secondProfileID := uuid.NewString()
	secondKeyPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	firstRunID := uuid.NewString()
	secondRunID := uuid.NewString()
	firstHypothesisID := uuid.NewString()
	secondHypothesisID := uuid.NewString()
	firstNullHashHypothesisID := uuid.NewString()
	secondNullHashHypothesisID := uuid.NewString()

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role
			) VALUES (
				$1::uuid, $2::uuid, $3, $4, $5, $6, ARRAY['read','write']::text[], 'member'
			)
		`, secondProfileID, teamID, "hash-"+secondProfileID, secondKeyPrefix, secondKeyPrefix[:6], "migration-profile-second"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
			ON CONFLICT (team_id) DO NOTHING
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid), ($1::uuid, $3::uuid)
			ON CONFLICT (team_id, profile_id) DO NOTHING
		`, teamID, firstProfileID, secondProfileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dream_cycle_runs (
				team_id, run_id, owner_profile_id, run_date, window_key, status
			) VALUES
				($1::uuid, $2::uuid, $3::uuid, '2026-07-30', '2026-07-30', 'failed'),
				($1::uuid, $4::uuid, $5::uuid, '2026-07-30', '2026-07-30', 'completed')
		`, teamID, firstRunID, firstProfileID, secondRunID, secondProfileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hypotheses (
				team_id, hypothesis_id, owner_profile_id, status, statement,
				content_hash, cycle_run_id, source_refs, source_versions,
				source_owner_profile_ids
			) VALUES
				($1::uuid, $2::uuid, $3::uuid, 'proposed', 'legacy team hypothesis',
				 'sha256:legacy-team-hypothesis', $4::uuid,
				 '[{"type":"relationship","id":"source-a"}]'::jsonb,
				 '{"source-a":1}'::jsonb, ARRAY[$3::uuid]),
				($1::uuid, $5::uuid, $6::uuid, 'reinforced', 'legacy team hypothesis',
				 'sha256:legacy-team-hypothesis', $7::uuid,
					 '[{"type":"relationship","id":"source-b"}]'::jsonb,
					 '{"source-b":2}'::jsonb, ARRAY[$6::uuid])
		`, teamID, firstHypothesisID, firstProfileID, firstRunID, secondHypothesisID, secondProfileID, secondRunID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hypotheses (
				team_id, hypothesis_id, owner_profile_id, status, statement,
				content_hash, source_refs, source_versions, source_owner_profile_ids
			) VALUES
				($1::uuid, $2::uuid, $3::uuid, 'proposed', 'legacy null-hash hypothesis',
				 NULL, '[{"type":"relationship","id":"source-null"}]'::jsonb,
				 '{"source-null":1}'::jsonb, ARRAY[$3::uuid]),
				($1::uuid, $4::uuid, $5::uuid, 'reinforced', 'legacy null-hash hypothesis',
				 NULL, '[{"type":"relationship","id":"source-null"}]'::jsonb,
				 '{"source-null":1}'::jsonb, ARRAY[$5::uuid])
		`, teamID, firstNullHashHypothesisID, firstProfileID, secondNullHashHypothesisID, secondProfileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app_config (key, value)
			VALUES
				('DREAMING_REFLECT_ENABLED', 'true'),
				('DREAMING_REEVALUATE_ENABLED', 'true'),
				('DREAMING_DREAM_ENABLED', 'true')
			ON CONFLICT (key) DO UPDATE SET value = excluded.value
		`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE app_config
			SET value = CASE key
				WHEN 'DREAMING_ENABLED' THEN 'false'
				WHEN 'DREAMING_FORCE_ENABLED' THEN 'true'
				ELSE value
			END
			WHERE key IN ('DREAMING_ENABLED', 'DREAMING_FORCE_ENABLED')
		`); err != nil {
			return err
		}
		return nil
	}))

	var seededLegacyConfigCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM app_config
		WHERE key IN (
			'DREAMING_REFLECT_ENABLED',
			'DREAMING_REEVALUATE_ENABLED',
			'DREAMING_DREAM_ENABLED'
		)
	`).Scan(&seededLegacyConfigCount))
	require.Equal(t, 3, seededLegacyConfigCount)

	m := NewMigratorWithDB(sqlDB)
	require.NoError(t, m.RunUp(ctx))

	var (
		canonicalRunCount        int
		canonicalHypothesisCount int
		canonicalRunOwner        string
		canonicalHypothesisOwner string
		canonicalStatus          string
		sourceRefCount           int
		sourceVersionCount       int
		sourceOwnerCount         int
		aliasCount               int
		canonicalNullHashCount   int
		nullHashAliasCount       int
		backfilledHash           string
		enabled                  string
		legacyConfigCount        int
	)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM dream_cycle_runs
		WHERE team_id = $1::uuid
		  AND window_key = '2026-07-30'
		  AND canonical_run_id IS NULL
	`, teamID).Scan(&canonicalRunCount))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(initiated_by_profile_id::text, '')
		FROM dream_cycle_runs
		WHERE team_id = $1::uuid
		  AND window_key = '2026-07-30'
		  AND canonical_run_id IS NULL
		LIMIT 1
	`, teamID).Scan(&canonicalRunOwner))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM hypotheses
		WHERE team_id = $1::uuid
		  AND content_hash = 'sha256:legacy-team-hypothesis'
		  AND canonical_hypothesis_id IS NULL
	`, teamID).Scan(&canonicalHypothesisCount))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(created_by_profile_id::text, ''), status,
		       jsonb_array_length(source_refs),
		       (SELECT count(*) FROM jsonb_object_keys(source_versions)),
		       cardinality(source_owner_profile_ids)
		FROM hypotheses
		WHERE team_id = $1::uuid
		  AND content_hash = 'sha256:legacy-team-hypothesis'
		  AND canonical_hypothesis_id IS NULL
		LIMIT 1
	`, teamID).Scan(
		&canonicalHypothesisOwner,
		&canonicalStatus,
		&sourceRefCount,
		&sourceVersionCount,
		&sourceOwnerCount,
	))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM hypotheses
		WHERE team_id = $1::uuid
		  AND content_hash = 'sha256:legacy-team-hypothesis'
		  AND canonical_hypothesis_id IS NOT NULL
	`, teamID).Scan(&aliasCount))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM hypotheses
		WHERE team_id = $1::uuid
		  AND statement = 'legacy null-hash hypothesis'
		  AND canonical_hypothesis_id IS NULL
	`, teamID).Scan(&canonicalNullHashCount))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT content_hash
		FROM hypotheses
		WHERE team_id = $1::uuid
		  AND statement = 'legacy null-hash hypothesis'
		  AND canonical_hypothesis_id IS NULL
		LIMIT 1
	`, teamID).Scan(&backfilledHash))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM hypotheses
		WHERE team_id = $1::uuid
		  AND statement = 'legacy null-hash hypothesis'
		  AND canonical_hypothesis_id IS NOT NULL
	`, teamID).Scan(&nullHashAliasCount))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT value
		FROM app_config
		WHERE key = 'DREAMING_ENABLED'
	`).Scan(&enabled))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM app_config
		WHERE key IN (
			'DREAMING_REFLECT_ENABLED',
			'DREAMING_REEVALUATE_ENABLED',
			'DREAMING_DREAM_ENABLED'
		)
	`).Scan(&legacyConfigCount))

	assert.Equal(t, 1, canonicalRunCount)
	assert.Equal(t, secondProfileID, canonicalRunOwner)
	assert.Equal(t, 1, canonicalHypothesisCount)
	assert.Equal(t, secondProfileID, canonicalHypothesisOwner)
	assert.Equal(t, "reinforced", canonicalStatus)
	assert.Equal(t, 2, sourceRefCount)
	assert.Equal(t, 2, sourceVersionCount)
	assert.Equal(t, 2, sourceOwnerCount)
	assert.Equal(t, 1, aliasCount)
	assert.Equal(t, 1, canonicalNullHashCount)
	assert.Equal(t, 1, nullHashAliasCount)
	assert.True(t, strings.HasPrefix(backfilledHash, "sha256:"))
	assert.Equal(t, "true", enabled)
	assert.Zero(t, legacyConfigCount)
}
