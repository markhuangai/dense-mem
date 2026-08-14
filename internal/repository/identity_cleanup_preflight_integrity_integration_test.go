package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIdentityCleanupPreflightRejectsReplacedBridgeFunctionBodies(t *testing.T) {
	for _, tt := range []struct {
		name     string
		function string
	}{
		{name: "legacy profile bridge", function: "dense_mem_sync_legacy_profile_identity"},
		{name: "sso identity bridge", function: "dense_mem_sync_sso_identity"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			replacement := `CREATE OR REPLACE FUNCTION ` + tt.function + `()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    RETURN NEW;
END;
$$`
			require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
				return tx.Exec(replacement).Error
			}))

			report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(context.Background())
			require.NoError(t, err)
			require.False(t, report.Ready)
			require.NotEmpty(t, report.Blockers)
			require.Equal(t, "identity_bridge_integrity_invalid", report.Blockers[0].Code)
		})
	}
}

func TestIdentityCleanupPreflightRejectsChangedBridgeFunctionExecutionAttributes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		alter string
	}{
		{name: "legacy security invoker", alter: "ALTER FUNCTION dense_mem_sync_legacy_profile_identity() SECURITY INVOKER"},
		{name: "legacy search path", alter: "ALTER FUNCTION dense_mem_sync_legacy_profile_identity() RESET search_path"},
		{name: "sso security invoker", alter: "ALTER FUNCTION dense_mem_sync_sso_identity() SECURITY INVOKER"},
		{name: "sso search path", alter: "ALTER FUNCTION dense_mem_sync_sso_identity() RESET search_path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
				return tx.Exec(tt.alter).Error
			}))

			report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(context.Background())
			require.NoError(t, err)
			require.False(t, report.Ready)
			require.NotEmpty(t, report.Blockers)
			require.Equal(t, "identity_bridge_integrity_invalid", report.Blockers[0].Code)
		})
	}
}

func TestIdentityCleanupPreflightRejectsConditionalBridgeTriggers(t *testing.T) {
	for _, tt := range []struct {
		name    string
		table   string
		trigger string
		create  string
	}{
		{
			name:    "legacy profile trigger",
			table:   "team_profiles",
			trigger: "team_profiles_identity_bridge",
			create: `
				CREATE TRIGGER team_profiles_identity_bridge
				AFTER INSERT OR UPDATE OF team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit, expires_at, revoked_at, last_used_at, auth_source, is_system, sso_identity_id, sso_provider_id, sso_subject, sso_email, sso_group_id, sso_entitlement_status, sso_owner_identity_id OR DELETE ON team_profiles
				FOR EACH ROW WHEN (false)
				EXECUTE FUNCTION dense_mem_sync_legacy_profile_identity()
			`,
		},
		{
			name:    "sso identity trigger",
			table:   "sso_identities",
			trigger: "sso_identities_identity_bridge",
			create: `
				CREATE TRIGGER sso_identities_identity_bridge
				AFTER INSERT OR UPDATE OF provider_id, subject, external_id, display_name, active OR DELETE ON sso_identities
				FOR EACH ROW WHEN (false)
				EXECUTE FUNCTION dense_mem_sync_sso_identity()
			`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adminDB, _, rls, cleanup := setupLedgerRepositoryDB(t)
			defer cleanup()
			require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
				if err := tx.Exec("DROP TRIGGER " + tt.trigger + " ON " + tt.table).Error; err != nil {
					return err
				}
				return tx.Exec(tt.create).Error
			}))

			report, err := NewIdentityCleanupPreflightRepository(adminDB, rls).ReadIdentityCleanupPreflight(context.Background())
			require.NoError(t, err)
			require.False(t, report.Ready)
			require.NotEmpty(t, report.Blockers)
			require.Equal(t, "identity_bridge_missing", report.Blockers[0].Code)
		})
	}
}
