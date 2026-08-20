//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const oauthProtectedResourceMigrationVersion int64 = 2026081901

func TestOAuthProtectedResourceMigrationDefaultsDormantAndRetainsSystemRLS(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, 2026081801)

	providerID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id, enabled)
			VALUES ($1, 'oauth migration provider', 'generic_oidc', 'https://issuer.example.test', 'client', true)
		`, providerID)
		return err
	}))
	require.NoError(t, migrationUpTo(ctx, db, oauthProtectedResourceMigrationVersion))

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var config []byte
		if err := tx.QueryRowContext(ctx, `SELECT protected_resource_config FROM sso_providers WHERE id = $1`, providerID).Scan(&config); err != nil {
			return err
		}
		require.JSONEq(t, `{
			"enabled": false,
			"audiences": [],
			"jwks_source": "discovery",
			"jwks_uri": "",
			"algorithms": ["RS256"],
			"scope_claim": "scope",
			"scope_mappings": [],
			"team_claim": ""
		}`, string(config))

		var baseURL string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key = 'MCP_PUBLIC_BASE_URL'`).Scan(&baseURL); err != nil {
			return err
		}
		require.Empty(t, baseURL)

		var indexExists bool
		if err := tx.QueryRowContext(ctx, `
			SELECT to_regclass('public.idx_sso_providers_protected_resource_enabled') IS NOT NULL
		`).Scan(&indexExists); err != nil {
			return err
		}
		require.True(t, indexExists)

		var constraintValidated bool
		if err := tx.QueryRowContext(ctx, `
			SELECT convalidated
			FROM pg_constraint
			WHERE conrelid = 'sso_providers'::regclass
			  AND conname = 'sso_providers_protected_resource_object'
		`).Scan(&constraintValidated); err != nil {
			return err
		}
		require.False(t, constraintValidated)
		return nil
	}))

	roleName := "dense_mem_oauth_rls_" + uuid.NewString()[:8]
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := db.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("migration RLS test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() {
		_, _ = db.ExecContext(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = db.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole)
	}()
	_, err := db.ExecContext(ctx, "GRANT SELECT, UPDATE ON sso_providers, app_config TO "+quotedRole)
	require.NoError(t, err)

	providers, configs, updated, err := oauthMigrationRoleVisibility(ctx, db, quotedRole, "profile", providerID)
	require.NoError(t, err)
	require.Zero(t, providers)
	require.Zero(t, configs)
	require.Zero(t, updated)

	providers, configs, updated, err = oauthMigrationRoleVisibility(ctx, db, quotedRole, "system", providerID)
	require.NoError(t, err)
	require.Equal(t, 1, providers)
	require.Equal(t, 1, configs)
	require.Equal(t, int64(1), updated)
}

func TestOAuthProtectedResourceMigrationGuardsCustomizedRollback(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, oauthProtectedResourceMigrationVersion)

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE app_config SET value = 'https://memory.example.test' WHERE key = 'MCP_PUBLIC_BASE_URL'`)
		return err
	}))
	err := migrationDownTo(ctx, db, 2026081801)
	require.ErrorContains(t, err, "refusing OAuth protected-resource rollback with customized MCP_PUBLIC_BASE_URL")

	providerID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE app_config SET value = '' WHERE key = 'MCP_PUBLIC_BASE_URL'`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sso_providers (
				id, name, kind, issuer_url, client_id, enabled, protected_resource_config
			) VALUES (
				$1, 'custom oauth provider', 'generic_oidc', 'https://issuer.example.test', 'client', true,
				'{"enabled":true}'::jsonb
			)
		`, providerID)
		return err
	}))
	err = migrationDownTo(ctx, db, 2026081801)
	require.ErrorContains(t, err, "refusing OAuth protected-resource rollback with customized provider configuration")

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE sso_providers
			SET protected_resource_config = '{
				"enabled": false,
				"audiences": [],
				"jwks_source": "discovery",
				"jwks_uri": "",
				"algorithms": ["RS256"],
				"scope_claim": "scope",
				"scope_mappings": [],
				"team_claim": ""
			}'::jsonb
			WHERE id = $1
		`, providerID)
		return err
	}))
	require.NoError(t, migrationDownTo(ctx, db, 2026081801))

	var columnExists bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'sso_providers'
			  AND column_name = 'protected_resource_config'
		)
	`).Scan(&columnExists))
	require.False(t, columnExists)

	var configExists bool
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM app_config WHERE key = 'MCP_PUBLIC_BASE_URL')`).Scan(&configExists)
	}))
	require.False(t, configExists)
}

func oauthMigrationRoleVisibility(ctx context.Context, db *sql.DB, quotedRole, mode string, providerID uuid.UUID) (int, int, int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
		return 0, 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tx_mode', $1, true)`, mode); err != nil {
		return 0, 0, 0, err
	}
	var providers int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sso_providers WHERE id = $1`, providerID).Scan(&providers); err != nil {
		return 0, 0, 0, err
	}
	var configs int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM app_config WHERE key = 'MCP_PUBLIC_BASE_URL'`).Scan(&configs); err != nil {
		return 0, 0, 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE app_config SET value = value WHERE key = 'MCP_PUBLIC_BASE_URL'`)
	if err != nil {
		return 0, 0, 0, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, 0, 0, err
	}
	return providers, configs, updated, tx.Commit()
}
