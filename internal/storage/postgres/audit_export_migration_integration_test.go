//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAuditExportMigrationPreservesHistoricalRowsAndRollback(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	provider, err := newRuntimeMigrationProvider(getMigrationsDir(), db, false)
	require.NoError(t, err)
	_, err = provider.UpTo(ctx, 20261006010002)
	require.NoError(t, err)
	id := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(id,operation,entity_type,entity_id,after_payload) VALUES($1::uuid,'CREATE','profile',$1::uuid::text,'{"content":"historical-exact-payload"}')`, id)
		return err
	}))
	var beforeNode, afterNode uint32
	require.NoError(t, db.QueryRowContext(ctx, "SELECT relfilenode FROM pg_class WHERE oid='audit_log'::regclass").Scan(&beforeNode))
	_, err = provider.UpTo(ctx, 20261008010002)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT relfilenode FROM pg_class WHERE oid='audit_log'::regclass").Scan(&afterNode))
	require.Equal(t, beforeNode, afterNode, "migration must not rewrite the audit table")
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var xid, payload string
		if err := tx.QueryRowContext(ctx, "SELECT insertion_xid::text,after_payload::text FROM audit_log WHERE id=$1", id).Scan(&xid, &payload); err != nil {
			return err
		}
		require.Equal(t, "0", xid)
		require.Contains(t, payload, "historical-exact-payload")
		var assigned, current string
		if err := tx.QueryRowContext(ctx, `INSERT INTO audit_log(operation,entity_type,entity_id,insertion_xid) VALUES('CREATE','profile','test','0') RETURNING insertion_xid::text,pg_current_xact_id()::text`).Scan(&assigned, &current); err != nil {
			return err
		}
		require.Equal(t, current, assigned)
		require.NotEqual(t, "0", assigned)
		return nil
	}))
	var indexes int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_index WHERE indexrelid IN ('audit_export_instance_idx'::regclass,'audit_export_team_idx'::regclass) AND indisvalid`).Scan(&indexes))
	require.Equal(t, 2, indexes)
	_, err = provider.DownTo(ctx, 20261006010002)
	require.NoError(t, err)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var retained string
		err := tx.QueryRowContext(ctx, "SELECT insertion_xid::text FROM audit_log WHERE id=$1", id).Scan(&retained)
		require.Equal(t, "0", retained)
		return err
	}))
	_, err = provider.UpTo(ctx, 20261008010002)
	require.NoError(t, err)
}
