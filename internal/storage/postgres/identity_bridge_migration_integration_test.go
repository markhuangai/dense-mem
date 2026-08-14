//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIdentityBridgeBackfillsStableIDsAndLegacyGovernance(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026080905)

	teamID := uuid.New()
	profileID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1, 'bridge-team')`, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit)
			VALUES ($1, $2, 'hash', 'dm_bridge_key', 'suffix', 'bridge-key', ARRAY['read','write'], 'manager', 17)
		`, profileID, teamID)
		return err
	}))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081001))

	var actorID, credentialID, aliasID uuid.UUID
	var admin bool
	var rateLimit int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT id FROM actor_identities WHERE id = $1`, profileID).Scan(&actorID))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT id FROM credentials WHERE legacy_profile_id = $1`, profileID).Scan(&credentialID))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT legacy_owner_id FROM ownership_aliases WHERE team_id = $1 AND legacy_owner_id = $2`, teamID, profileID).Scan(&aliasID))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT rate_limit FROM credentials WHERE legacy_profile_id = $1`, profileID).Scan(&rateLimit))
	require.Equal(t, profileID, actorID)
	require.Equal(t, profileID, credentialID)
	require.Equal(t, profileID, aliasID)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT team_admin FROM team_memberships WHERE legacy_profile_id = $1`, profileID).Scan(&admin))
	require.True(t, admin)
	require.Equal(t, 17, rateLimit)
}

func TestMigrationStartupClassifierCoversFreshLegacyAndCompatibleStates(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	state, err := ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateFresh, state.Kind)

	files, err := ListMigrationFiles(getMigrationsDir())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 2)
	runGooseUpTo(t, ctx, sqlDB, files[len(files)-2].Version)
	state, err = ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateLegacy, state.Kind)
	require.True(t, state.ExactLegacy)

	runGooseUpTo(t, ctx, sqlDB, files[len(files)-1].Version)
	state, err = ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateCompatible, state.Kind)
	require.NoError(t, ValidateStartupMigrationState(ctx, sqlDB, getMigrationsDir()))
}

func TestMigrationStartupRejectsMissingBridgeRelations(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TABLE membership_grants, identity_external_links`)
		return err
	}))

	state, err := ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateInvalid, state.Kind)
	require.Equal(t, "identity bridge is only partially installed", state.Reason)
}

func TestMigrationStartupRejectsMissingBridgeTriggers(t *testing.T) {
	for _, trigger := range []string{"team_profiles_identity_bridge", "sso_identities_identity_bridge"} {
		t.Run(trigger, func(t *testing.T) {
			ctx := context.Background()
			sqlDB, cleanup := openMigrationSQLDB(t, ctx)
			defer cleanup()
			runGooseUpTo(t, ctx, sqlDB, 2026081001)

			require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `DROP TRIGGER `+trigger+` ON `+map[string]string{
					"team_profiles_identity_bridge":  "team_profiles",
					"sso_identities_identity_bridge": "sso_identities",
				}[trigger])
				return err
			}))

			state, err := ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
			require.NoError(t, err)
			require.Equal(t, MigrationStateInvalid, state.Kind)
			require.Equal(t, "identity bridge is only partially installed", state.Reason)
		})
	}
}

func TestMigrationStartupRejectsDisabledBridgeTriggers(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `ALTER TABLE team_profiles DISABLE TRIGGER team_profiles_identity_bridge`)
		return err
	}))

	state, err := ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateInvalid, state.Kind)
	require.Equal(t, "identity bridge is only partially installed", state.Reason)
}

func TestIdentityBridgeReconcilesLegacyWritesAndRLS(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	teamA, teamB, profileID := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1, 'team-a'), ($2, 'team-b')`, teamA, teamB); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, name, scopes)
			VALUES ($1, $2, 'hash', 'dm_bridge_write', 'bridge-write', ARRAY['read'])
			`, profileID, teamA)
		return err
	}))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE team_profiles SET rate_limit = 23 WHERE id = $1`, profileID)
		return err
	}))
	var rateLimit int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT rate_limit FROM credentials WHERE legacy_profile_id = $1`, profileID).Scan(&rateLimit))
	require.Equal(t, 23, rateLimit)
	roleName := "dense_mem_identity_bridge_rls"
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := sqlDB.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("identity bridge RLS test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() { _, _ = sqlDB.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole) }()
	for _, grant := range []string{
		"GRANT USAGE ON SCHEMA public TO " + quotedRole,
		"GRANT SELECT ON credentials TO " + quotedRole,
	} {
		require.NoError(t, func() error { _, err := sqlDB.ExecContext(ctx, grant); return err }())
	}

	var count int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true), set_config('app.current_profile_id', $1, true)`, teamA); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE team_id = $1`, teamA).Scan(&count)
	}))
	require.Equal(t, 1, count)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true), set_config('app.current_profile_id', $1, true)`, teamB); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE team_id = $1`, teamA).Scan(&count)
	}))
	require.Zero(t, count)
}
