//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	densecrypto "github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/stretchr/testify/require"
)

func TestIdentityCleanupComposeSeed(t *testing.T) {
	variant := os.Getenv("DENSE_MEM_E2E_IDENTITY_SEED_VARIANT")
	if variant == "" {
		t.Skip("compose identity seed is only run by the shared E2E controller")
	}
	versionByVariant := map[string]int64{
		"v2_4_8":       2026080905,
		"bridge":       2026081001,
		"bridge_valid": 2026081001,
	}
	version, ok := versionByVariant[variant]
	require.True(t, ok, "unsupported compose identity seed variant %q", variant)

	teamID := uuid.MustParse(requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_IDENTITY_TEAM_ID"))
	profileID := uuid.MustParse(requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_IDENTITY_PROFILE_ID"))
	rawKey := requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_IDENTITY_API_KEY")
	keyHash, err := densecrypto.HashKey(rawKey)
	require.NoError(t, err)

	sqlDB := openIdentitySeedDB(t)
	require.NoError(t, migrationUpTo(context.Background(), sqlDB, version))
	seedIdentityUpgradeState(t, sqlDB, variant, teamID, profileID, rawKey, keyHash)
}

func openIdentitySeedDB(t *testing.T) *sql.DB {
	t.Helper()
	host := os.Getenv("DENSE_MEM_E2E_POSTGRES_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	connectionURL := &url.URL{
		Scheme: "postgresql",
		User: url.UserPassword(
			requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_POSTGRES_USER"),
			requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_POSTGRES_PASSWORD"),
		),
		Host: net.JoinHostPort(host, requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_POSTGRES_PORT")),
		Path: requiredIdentitySeedEnv(t, "DENSE_MEM_E2E_POSTGRES_DB"),
	}
	query := connectionURL.Query()
	query.Set("sslmode", "disable")
	connectionURL.RawQuery = query.Encode()

	sqlDB, err := sql.Open("pgx", connectionURL.String())
	require.NoError(t, err)
	require.NoError(t, sqlDB.PingContext(context.Background()))
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return sqlDB
}

func requiredIdentitySeedEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	require.NotEmpty(t, value, "%s is required", name)
	return value
}

func seedIdentityUpgradeState(
	t *testing.T,
	sqlDB *sql.DB,
	variant string,
	teamID uuid.UUID,
	profileID uuid.UUID,
	rawKey string,
	keyHash string,
) {
	t.Helper()
	ctx := context.Background()
	providerID, identityID, ssoProfileID := uuid.New(), uuid.New(), uuid.New()
	portalHash := "identity-upgrade-portal-" + uuid.NewString()
	ssoSessionHash := "identity-upgrade-sso-" + uuid.NewString()
	now := time.Now().UTC()

	tx, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		SELECT set_config('app.tx_mode', 'system', true),
		       set_config('app.current_team_id', $1, true),
		       set_config('app.current_profile_id', $2, true)
	`, teamID.String(), profileID.String())
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1, $2)`, teamID, "identity-upgrade-"+variant+"-"+teamID.String())
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO v2_compatibility_markers (
			marker_id, marker_kind, version, status, corpus_hash, gate_report_hash, metadata
		) VALUES ($1, 'v2_cutover', 'dense-mem.v2.1.cutover.v1', 'compatible', '', '', '{"fixture":"identity_upgrade"}'::jsonb)
	`, uuid.New())
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_profiles (
			id, team_id, key_hash, key_prefix, key_suffix, name, scopes, role,
			rate_limit, auth_source, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, ARRAY['read','write']::text[], 'manager', 300, 'api_key', $7, $7)
	`, profileID, teamID, keyHash, densecrypto.GetKeyPrefix(rawKey), densecrypto.GetKeySuffix(rawKey), "identity-upgrade-key", now)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
		VALUES ($1, $2, 'generic_oidc', 'https://identity-upgrade.invalid', 'identity-upgrade-client')
	`, providerID, "identity-upgrade-provider-"+providerID.String())
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
		VALUES ($1, $2, $3, $3, 'upgrade@example.invalid', 'Identity Upgrade', true)
	`, identityID, providerID, "identity-upgrade-subject-"+identityID.String())
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_profiles (
			id, team_id, name, scopes, role, auth_source, sso_identity_id,
			sso_provider_id, sso_subject, sso_email, sso_group_id, sso_entitlement_status
		) VALUES (
			$1, $2, 'Identity Upgrade SSO', ARRAY['read']::text[], 'member', 'sso', $3,
			$4, $5, 'upgrade@example.invalid', 'identity-upgrade-group', 'active'
		)
	`, ssoProfileID, teamID, identityID, providerID, "identity-upgrade-subject-"+identityID.String())
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `INSERT INTO semantic_team_refs (team_id) VALUES ($1)`, teamID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1, $2), ($1, $3)
	`, teamID, profileID, ssoProfileID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO usage_metric_buckets (
			bucket_start, team_id, key_id, route, method, status_class, request_count
		) VALUES (date_trunc('hour', $1::timestamptz), $2, $3, '/identity-upgrade', 'POST', 2, 2)
	`, now, teamID, profileID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO usage_metric_buckets (
			bucket_start, team_id, key_id, route, method, status_class, request_count
		) VALUES (date_trunc('hour', $1::timestamptz), $2, $3, '/identity-upgrade-sso', 'GET', 2, 1)
	`, now, teamID, ssoProfileID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO user_portal_sessions (session_hash, key_id, csrf_hash, expires_at)
		VALUES ($1, $2, 'identity-upgrade-csrf', $3::timestamptz + interval '1 hour')
	`, portalHash, profileID, now)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sso_sessions (
			session_hash, identity_id, provider_id, team_profile_id, team_id, csrf_hash, expires_at
		) VALUES ($1, $2, $3, $4, $5, 'identity-upgrade-sso-csrf', $6::timestamptz + interval '1 hour')
	`, ssoSessionHash, identityID, providerID, ssoProfileID, teamID, now)
	require.NoError(t, err)

	if variant == "bridge" || variant == "bridge_valid" {
		_, err = tx.ExecContext(ctx, `UPDATE team_profiles SET scopes = ARRAY['read']::text[] WHERE id = $1`, profileID)
		require.NoError(t, err)
	}
	if variant == "bridge" {
		mismatchTeamID := uuid.New()
		_, err = tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1, $2)`, mismatchTeamID, "identity-upgrade-mismatch-"+mismatchTeamID.String())
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (date_trunc('hour', $1::timestamptz), $2, $3, '/identity-cleanup-mismatch', 'POST', 2, 1)
		`, now, mismatchTeamID, ssoProfileID)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

func TestIdentityCleanupUpgradesPopulatedPreBridgeDatabase(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026080905)

	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	providerID, identityID, ssoProfileID := insertIdentityCleanupSSOProfile(t, ctx, sqlDB, teamID)
	sessionHash := "identity-cleanup-sso-" + uuid.NewString()
	portalHash := "identity-cleanup-portal-" + uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)
			ON CONFLICT (team_id) DO NOTHING
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid), ($1::uuid, $3::uuid)
		`, teamID, profileID, ssoProfileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (date_trunc('hour', now()), $1::uuid, $2::uuid, '/mcp', 'POST', 2, 3)
		`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (date_trunc('hour', now()), $1::uuid, $2::uuid, '/sso', 'GET', 2, 1)
		`, teamID, ssoProfileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_portal_sessions (session_hash, key_id, csrf_hash, expires_at)
			VALUES ($1, $2::uuid, 'portal-csrf', now() + interval '1 hour')
		`, portalHash, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sso_sessions (
				session_hash, identity_id, provider_id, team_profile_id, team_id,
				csrf_hash, expires_at
			) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
				'sso-csrf', now() + interval '1 hour')
		`, sessionHash, identityID, providerID, ssoProfileID, teamID)
		return err
	}))

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081501))
	require.False(t, tableExists(t, ctx, sqlDB, "team_profiles"))
	require.False(t, tableExists(t, ctx, sqlDB, "identity_compatibility_state"))
	require.False(t, columnExists(t, ctx, sqlDB, "credentials", "legacy_profile_id"))
	require.False(t, columnExists(t, ctx, sqlDB, "team_memberships", "legacy_profile_id"))

	var keyHash, credentialStatus string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT key_hash, status FROM credentials WHERE id = $1::uuid
	`, profileID).Scan(&keyHash, &credentialStatus))
	require.Equal(t, "hash-"+profileID, keyHash)
	require.Equal(t, "active", credentialStatus)

	var aliasIdentity, sessionIdentity, ssoProfileName string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT canonical_identity_id::text
		FROM ownership_aliases
		WHERE team_id = $1::uuid AND legacy_owner_id = $2::uuid
	`, teamID, ssoProfileID).Scan(&aliasIdentity))
	require.Equal(t, identityID, aliasIdentity)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT membership.actor_identity_id::text
		FROM sso_sessions AS session
		JOIN team_memberships AS membership ON membership.id = session.membership_id
		WHERE session.session_hash = $1
	`, sessionHash).Scan(&sessionIdentity))
	require.Equal(t, identityID, sessionIdentity)
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT sso_profile_name
		FROM team_memberships
		WHERE team_id = $1::uuid AND actor_identity_id = $2::uuid
	`, teamID, identityID).Scan(&ssoProfileName))
	require.Equal(t, "Cleanup SSO", ssoProfileName)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (date_trunc('hour', now()), $1::uuid, $2::uuid, '/sso-post-cleanup', 'GET', 1, 1)
		`, teamID, ssoProfileID)
		return err
	}))

	for query, argument := range map[string]string{
		`SELECT count(*) FROM semantic_profile_refs WHERE team_id = $1::uuid`:                      teamID,
		`SELECT count(*) FROM usage_metric_buckets WHERE key_id = $1::uuid AND route = '/mcp'`:     profileID,
		`SELECT count(*) FROM usage_metric_buckets WHERE key_id = $1::uuid AND route LIKE '/sso%'`: ssoProfileID,
		`SELECT count(*) FROM user_portal_sessions WHERE session_hash = $1`:                        portalHash,
		`SELECT count(*) FROM membership_grants WHERE grant_name IN ('read','write')`:              "",
	} {
		var count int
		if argument == "" {
			require.NoError(t, sqlDB.QueryRowContext(ctx, query).Scan(&count))
		} else {
			require.NoError(t, sqlDB.QueryRowContext(ctx, query, argument).Scan(&count))
		}
		require.Positive(t, count)
	}
	repositoryLatest, err := latestMigrationVersion(getMigrationsDir())
	require.NoError(t, err)
	require.NoError(t, migrationUpTo(ctx, sqlDB, repositoryLatest))
	require.NoError(t, ValidateStartupMigrationState(ctx, sqlDB, getMigrationsDir()))
}

func TestIdentityCleanupReconcilesBridgeWriteBeforeDrop(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)`, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)
		`, teamID, profileID)
		return err
	}))

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081501))
	var credentialCount, aliasCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE id = $1::uuid`, profileID).Scan(&credentialCount))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*) FROM ownership_aliases WHERE team_id = $1::uuid AND legacy_owner_id = $2::uuid
	`, teamID, profileID).Scan(&aliasCount))
	require.Equal(t, 1, credentialCount)
	require.Equal(t, 1, aliasCount)
}

func TestIdentityCleanupDisablesCredentialDeletedDuringBridgeWindow(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM team_profiles WHERE id = $1::uuid`, profileID)
		return err
	}))

	var status string
	var legacyProfileID sql.NullString
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status, legacy_profile_id::text
		FROM credentials
		WHERE id = $1::uuid
	`, profileID).Scan(&status, &legacyProfileID))
	require.Equal(t, "revoked", status)
	require.False(t, legacyProfileID.Valid)

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081501))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT status
		FROM credentials
		WHERE id = $1::uuid AND team_id = $2::uuid
	`, profileID, teamID).Scan(&status))
	require.Equal(t, "disabled", status)
}

func TestIdentityCleanupMismatchRollsBackAndLeavesBridge(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	profileTeamID := uuid.NewString()
	usageTeamID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1::uuid, 'cleanup-mismatch-profile')`, profileTeamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1::uuid, 'cleanup-mismatch-usage')`, usageTeamID)
		return err
	}))
	_, _, ssoProfileID := insertIdentityCleanupSSOProfile(t, ctx, sqlDB, profileTeamID)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_metric_buckets (
				bucket_start, team_id, key_id, route, method, status_class, request_count
			) VALUES (date_trunc('hour', now()), $1::uuid, $2::uuid, '/mcp', 'POST', 2, 1)
		`, usageTeamID, ssoProfileID)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, 2026081501)
	require.ErrorContains(t, err, "usage history missing ownership aliases")
	require.True(t, tableExists(t, ctx, sqlDB, "team_profiles"))
	require.True(t, tableExists(t, ctx, sqlDB, "identity_compatibility_state"))
	require.True(t, columnExists(t, ctx, sqlDB, "credentials", "legacy_profile_id"))
	require.False(t, identityCleanupMigrationApplied(t, ctx, sqlDB))
	state, classifyErr := ClassifyMigrationState(ctx, sqlDB, getMigrationsDir())
	require.NoError(t, classifyErr)
	require.Equal(t, MigrationStateBridge, state.Kind)
}

func TestIdentityCleanupLockContentionRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026081001)

	lockTx, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, func() error {
		_, lockErr := lockTx.ExecContext(ctx, `LOCK TABLE team_profiles IN ACCESS SHARE MODE`)
		return lockErr
	}())

	blockedCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	err = migrationUpTo(blockedCtx, sqlDB, 2026081501)
	cancel()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "55P03", pgErr.Code)
	require.Contains(t, pgErr.Message, "lock timeout")
	require.NoError(t, lockTx.Rollback())
	require.False(t, identityCleanupMigrationApplied(t, ctx, sqlDB))
	require.True(t, tableExists(t, ctx, sqlDB, "team_profiles"))
	require.True(t, tableExists(t, ctx, sqlDB, "identity_compatibility_state"))

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081501))
	require.True(t, identityCleanupMigrationApplied(t, ctx, sqlDB))
	require.False(t, tableExists(t, ctx, sqlDB, "team_profiles"))
}

func insertIdentityCleanupSSOProfile(t *testing.T, ctx context.Context, sqlDB *sql.DB, teamID string) (string, string, string) {
	t.Helper()
	providerID, identityID, profileID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES ($1::uuid, $2, 'generic_oidc', 'https://cleanup.invalid', 'cleanup-client')
		`, providerID, "cleanup-provider-"+providerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active)
			VALUES ($1::uuid, $2::uuid, $3, $3, 'cleanup@example.invalid', 'Cleanup User', true)
		`, identityID, providerID, "cleanup-subject-"+identityID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_profiles (
				id, team_id, key_hash, key_prefix, name, scopes, role, auth_source,
				sso_identity_id, sso_provider_id, sso_subject, sso_email, sso_group_id,
				sso_entitlement_status
			) VALUES (
				$1::uuid, $2::uuid, NULL, NULL, 'Cleanup SSO', ARRAY['read']::text[], 'member', 'sso',
				$3::uuid, $4::uuid, $5, 'cleanup@example.invalid', 'cleanup-group', 'active'
			)
		`, profileID, teamID, identityID, providerID, "cleanup-subject-"+identityID)
		return err
	}))
	return providerID, identityID, profileID
}

func identityCleanupMigrationApplied(t *testing.T, ctx context.Context, sqlDB *sql.DB) bool {
	t.Helper()
	var applied bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM goose_db_version
			WHERE version_id = 2026081501 AND is_applied
		)
	`).Scan(&applied))
	return applied
}
