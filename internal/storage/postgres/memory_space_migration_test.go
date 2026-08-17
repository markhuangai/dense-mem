//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemorySpacePrivateInitializerCanUpsertUnderForceRLS(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	require.NoError(t, NewMigratorWithDB(sqlDB).RunUp(ctx))
	teamID := uuid.New()
	ownerID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES ($1, $2, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, "memory-space-policy-test")
		return err
	}))

	var firstID, secondID uuid.UUID
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true)`, teamID.String()); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT dense_mem_ensure_private_space($1, 'credential_private', $2)`, teamID, ownerID).Scan(&firstID); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT dense_mem_ensure_private_space($1, 'credential_private', $2)`, teamID, ownerID).Scan(&secondID)
	}))
	assert.Equal(t, firstID, secondID, "private-space initialization should be idempotent")
}

func TestMemorySpaceBackfillPreservesAppendOnlyGuards(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081602))
	teamID, profileID, identityID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES ($1::uuid, $2, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, "memory-space-backfill-test")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES ($1::uuid, 'human', $2::uuid, 'migration owner')
		`, identityID, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid)
		`, teamID, profileID, identityID); err != nil {
			return err
		}
		ingestID, fragmentID := uuid.NewString(), uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'queued')
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, 0,
				'append-only migration fixture', $5, 'conversation', 'primary'
			)
		`, teamID, fragmentID, ingestID, profileID, "sha256:"+fragmentID)
		return err
	}))

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081701))
	var spaceID uuid.UUID
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT fragment.space_id
		FROM evidence_fragments AS fragment
		WHERE fragment.team_id = $1::uuid
	`, teamID).Scan(&spaceID))
	assert.NotEqual(t, uuid.Nil, spaceID, "existing append-only evidence must receive the shared space")
	var triggerEnabled bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT tgenabled = 'O'
		FROM pg_trigger
		WHERE tgname = 'evidence_fragments_append_only'
	`).Scan(&triggerEnabled))
	assert.True(t, triggerEnabled, "append-only guard must be restored after backfill")
}
