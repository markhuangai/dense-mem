//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
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
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role)
			VALUES ($1, $2, 'hash', 'dm_bridge_key', 'suffix', 'bridge-key', ARRAY['read','write'], 'manager')
		`, profileID, teamID)
		return err
	}))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081001))

	var actorID, credentialID, aliasID uuid.UUID
	var admin bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT id FROM actor_identities WHERE id = $1`, profileID).Scan(&actorID))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT id FROM credentials WHERE legacy_profile_id = $1`, profileID).Scan(&credentialID))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT legacy_owner_id FROM ownership_aliases WHERE team_id = $1 AND legacy_owner_id = $2`, teamID, profileID).Scan(&aliasID))
	require.Equal(t, profileID, actorID)
	require.Equal(t, profileID, credentialID)
	require.Equal(t, profileID, aliasID)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT team_admin FROM team_memberships WHERE legacy_profile_id = $1`, profileID).Scan(&admin))
	require.True(t, admin)
}

func TestMigrationStartupClassifierCoversFreshLegacyBridgeAndCleanStates(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	state, err := ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateFresh, state.Kind)

	repositoryLatest, err := latestMigrationVersion(getMigrationsDir())
	require.NoError(t, err)
	runGooseUpTo(t, ctx, sqlDB, 2026080905)
	state, err = ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateLegacy, state.Kind)

	runGooseUpTo(t, ctx, sqlDB, 2026081001)
	state, err = ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateBridge, state.Kind)

	runGooseUpTo(t, ctx, sqlDB, repositoryLatest)
	state, err = ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, err)
	require.Equal(t, MigrationStateClean, state.Kind)
	require.NoError(t, ValidateStartupMigrationState(ctx, sqlDB, getMigrationsDir()))
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
		"GRANT SELECT, UPDATE ON team_profiles TO " + quotedRole,
		"GRANT SELECT ON credentials, team_memberships, membership_grants TO " + quotedRole,
	} {
		require.NoError(t, func() error { _, err := sqlDB.ExecContext(ctx, grant); return err }())
	}
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true), set_config('app.current_profile_id', $2, true)`, teamA, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_profiles SET scopes = ARRAY['read','write'] WHERE id = $1`, profileID); err != nil {
			return err
		}
		var credentialScopes, membershipMaximum string
		if err := tx.QueryRowContext(ctx, `
			SELECT c.scopes::text, m.maximum_grants::text
			FROM credentials c
			JOIN team_memberships m ON m.legacy_profile_id = c.legacy_profile_id
			WHERE c.legacy_profile_id = $1 AND m.team_id = $2
		`, profileID, teamA).Scan(&credentialScopes, &membershipMaximum); err != nil {
			return err
		}
		if credentialScopes != "{read,write}" || membershipMaximum != "{read,write}" {
			return fmt.Errorf("legacy team-mode write was not reconciled: credentials=%s membership=%s", credentialScopes, membershipMaximum)
		}
		return nil
	}))

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

func TestIdentityBridgePreservesGovernanceAliasesAndSSOExternalLinks(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	teamID := uuid.New()
	profileID := uuid.New()
	providerID := uuid.New()
	identityID := uuid.New()
	ssoProfileID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1, 'bridge-reconcile-team')`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES ($1, 'bridge-provider', 'generic_oidc', 'https://issuer.invalid', 'bridge-client')
		`, providerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name)
			VALUES ($1, $2, 'bridge-subject', 'bridge-external', 'bridge@example.invalid', 'Bridge User')
		`, identityID, providerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (id, team_id, key_hash, key_prefix, name, scopes, role)
			VALUES ($1, $2, 'hash', 'dm_reconcile_key', 'bridge-key', ARRAY['read'], 'member')
		`, profileID, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, name, scopes, role,
				auth_source, sso_identity_id, sso_provider_id, sso_subject,
				sso_entitlement_status
			)
			VALUES ($1, $2, NULL, NULL, 'Bridge SSO', ARRAY['read'], 'manager',
				'sso', $3, $4, 'bridge-subject', 'active')
		`, ssoProfileID, teamID, identityID, providerID)
		return err
	}))

	var linkedIdentity uuid.UUID
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT identity_id
		FROM identity_external_links
		WHERE provider = $1::text AND external_id = 'bridge-external'
	`, providerID).Scan(&linkedIdentity))
	require.Equal(t, identityID, linkedIdentity)

	var canonicalIdentity uuid.UUID
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT actor_identity_id
		FROM team_memberships
		WHERE legacy_profile_id = $1
	`, ssoProfileID).Scan(&canonicalIdentity))
	require.Equal(t, identityID, canonicalIdentity)
	var actorTeamID sql.NullString
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT team_id FROM actor_identities WHERE id = $1`, identityID).Scan(&actorTeamID))
	require.False(t, actorTeamID.Valid, "SSO actor identity must remain team-neutral")
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true), set_config('app.current_profile_id', $2, true)`, teamID, ssoProfileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE team_profiles SET scopes = ARRAY['read','write'] WHERE id = $1`, ssoProfileID)
		return err
	}))
	var ssoMaximumGrants string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT maximum_grants::text FROM team_memberships WHERE actor_identity_id = $1 AND team_id = $2`, identityID, teamID).Scan(&ssoMaximumGrants))
	require.Equal(t, "{read,write}", ssoMaximumGrants)

	var aliasIdentity uuid.UUID
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT canonical_identity_id
		FROM ownership_aliases
		WHERE team_id = $1 AND legacy_owner_id = $2
	`, teamID, ssoProfileID).Scan(&aliasIdentity))
	require.Equal(t, identityID, aliasIdentity)

	var membershipID uuid.UUID
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT id FROM team_memberships WHERE legacy_profile_id = $1
	`, profileID).Scan(&membershipID))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO membership_grants (membership_id, grant_name, source)
			VALUES ($1, 'governance:manage', 'explicit')
		`, membershipID)
		return err
	}))

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE team_profiles SET scopes = ARRAY['read','write'] WHERE id = $1`, profileID)
		return err
	}))
	var grants int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM membership_grants
		WHERE membership_id = $1 AND grant_name IN ('read', 'write', 'governance:manage')
	`, membershipID).Scan(&grants))
	require.Equal(t, 3, grants)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM team_profiles WHERE id = $1`, profileID)
		return err
	}))
	var aliasCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*) FROM ownership_aliases WHERE team_id = $1 AND legacy_owner_id = $2
	`, teamID, profileID).Scan(&aliasCount))
	require.Equal(t, 1, aliasCount)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM team_profiles WHERE id = $1`, ssoProfileID)
		return err
	}))
	var ssoMembershipStatus string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status
		FROM team_memberships
		WHERE actor_identity_id = $1 AND team_id = $2
	`, identityID, teamID).Scan(&ssoMembershipStatus))
	require.Equal(t, "revoked", ssoMembershipStatus)
}

func TestIdentityBridgeKeepsSSOActorTeamNeutralAcrossMemberships(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026080905)

	teamA, teamB := uuid.New(), uuid.New()
	providerID, identityID := uuid.New(), uuid.New()
	apiProfileA, apiProfileB := uuid.New(), uuid.New()
	ssoProfileA, ssoProfileB := uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name) VALUES ($1, 'bridge-multi-team-a'), ($2, 'bridge-multi-team-b')
		`, teamA, teamB); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES ($1, 'bridge-multi-provider', 'generic_oidc', 'https://issuer.multi.invalid', 'bridge-multi-client')
		`, providerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name)
			VALUES ($1, $2, 'bridge-multi-subject', 'bridge-multi-external', 'multi@example.invalid', 'Bridge Multi User')
		`, identityID, providerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, name, scopes, sso_owner_identity_id
			) VALUES
				($1, $3, 'hash-a', 'dm_multi_key_a', 'multi-key-a', ARRAY['read'], $5),
				($2, $4, 'hash-b', 'dm_multi_key_b', 'multi-key-b', ARRAY['read'], $5)
		`, apiProfileA, apiProfileB, teamA, teamB, identityID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, name, scopes, role,
				auth_source, sso_identity_id, sso_provider_id, sso_subject,
				sso_entitlement_status
			) VALUES
				($1, $3, NULL, NULL, 'multi-sso-a', ARRAY['read'], 'member',
				 'sso', $5, $6, 'bridge-multi-subject', 'active'),
				($2, $4, NULL, NULL, 'multi-sso-b', ARRAY['read'], 'member',
				 'sso', $5, $6, 'bridge-multi-subject', 'active')
		`, ssoProfileA, ssoProfileB, teamA, teamB, identityID, providerID)
		return err
	}))
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081001))

	var actorTeam sql.NullString
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT team_id::text FROM actor_identities WHERE id = $1
	`, identityID).Scan(&actorTeam))
	require.False(t, actorTeam.Valid, "SSO actor identity must not be assigned to one team")

	var membershipCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*) FROM team_memberships WHERE actor_identity_id = $1
	`, identityID).Scan(&membershipCount))
	require.Equal(t, 2, membershipCount)

	updateProfile := func(profileID, teamID uuid.UUID, scopes string) {
		require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
				SELECT set_config('app.current_team_id', $1, true), set_config('app.current_profile_id', $2, true)
			`, teamID, profileID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE team_profiles SET scopes = $1 WHERE id = $2`, scopes, profileID)
			return err
		}))
	}
	updateProfile(ssoProfileB, teamB, "{read,write}")
	updateProfile(apiProfileA, teamA, "{read,write}")
	updateProfile(apiProfileB, teamB, "{read,write}")

	for _, profileID := range []uuid.UUID{apiProfileA, apiProfileB} {
		var ownerIdentity sql.NullString
		require.NoError(t, sqlDB.QueryRowContext(ctx, `
			SELECT owner_identity_id::text FROM credentials WHERE legacy_profile_id = $1
		`, profileID).Scan(&ownerIdentity))
		require.True(t, ownerIdentity.Valid)
		require.Equal(t, identityID.String(), ownerIdentity.String)
	}
}
