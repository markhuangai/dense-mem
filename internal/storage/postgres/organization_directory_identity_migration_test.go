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

func TestOrganizationDirectoryIdentityMigrationSeedsConfigAndBackfillsLegacyAzureIdentity(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073101)
	providerID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (
				id, name, kind, issuer_url, client_id, client_secret_env,
				scopes, group_claims, groups_endpoint, groups_scopes, enabled
			) VALUES (
				$1, 'legacy azure provider', 'azure_ad', 'https://login.example.test', 'legacy-client', '',
				ARRAY['openid']::text[], ARRAY['groups']::text[], '', ARRAY[]::text[], true
			)
		`, providerID)
		return err
	}))

	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026073103))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		var identityClaim string
		var tenantID string
		if err := tx.QueryRowContext(ctx, `SELECT identity_claim, tenant_id FROM sso_providers WHERE id = $1`, providerID).Scan(&identityClaim, &tenantID); err != nil {
			return err
		}
		require.Equal(t, "sub", identityClaim)
		require.Empty(t, tenantID)
		for _, key := range []string{"SCIM_PUBLIC_BASE_URL", "CONTROL_PUBLIC_BASE_URL"} {
			var value string
			if err := tx.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key = $1`, key).Scan(&value); err != nil {
				return err
			}
			require.Empty(t, value)
		}
		return nil
	}))
}

func TestOrganizationDirectoryIdentityMigrationUsesSystemRLSMode(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026073103)

	roleName := "dense_mem_org_identity_migration_rls"
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := sqlDB.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("migration RLS test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() { _, _ = sqlDB.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole) }()
	require.NoError(t, func() error {
		_, err := sqlDB.ExecContext(ctx, "GRANT USAGE ON SCHEMA public TO "+quotedRole)
		return err
	}())
	require.NoError(t, func() error {
		_, err := sqlDB.ExecContext(ctx, "GRANT INSERT ON app_config TO "+quotedRole)
		return err
	}())

	require.NoError(t, execMigrationRoleConfigInsert(ctx, sqlDB, quotedRole, "system", "ORGANIZATION_DIRECTORY_RLS_SYSTEM"))
	require.Error(t, execMigrationRoleConfigInsert(ctx, sqlDB, quotedRole, "migration", "ORGANIZATION_DIRECTORY_RLS_MIGRATION"))
}

func execMigrationRoleConfigInsert(ctx context.Context, db *sql.DB, quotedRole, mode, key string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tx_mode', $1, true)`, mode); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_config (key, value) VALUES ($1, '')`, key)
	if err != nil {
		return err
	}
	return tx.Commit()
}
