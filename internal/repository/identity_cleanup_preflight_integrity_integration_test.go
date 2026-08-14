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
