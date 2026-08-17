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
		placementRunID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (team_id, placement_run_id, ingest_id, owner_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'queued')
		`, teamID, placementRunID, ingestID, profileID); err != nil {
			return err
		}
		assessmentID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
				team_id, assessment_id, owner_profile_id, request_id,
				assessor_contract_version, model, tokenizer,
				input_tokens, output_tokens, candidate_context_tokens,
				normalized_response, response_hash, validated_at,
				assessment_scope, placement_run_id, ingest_id
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'migration-request',
				'migration-contract', 'migration-model', 'migration-tokenizer',
				0, 0, 0, '{}'::jsonb, 'migration-hash', now(),
				'submission', $4::uuid, $5::uuid
			)
		`, teamID, assessmentID, profileID, placementRunID, ingestID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO predicate_registration_events (
				team_id, placement_run_id, assessment_id, owner_profile_id,
				relationship_ref, registration_action, predicate_key, predicate_version
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'migration-rel', 'created', 'migration_predicate', 1)
		`, teamID, placementRunID, assessmentID, profileID); err != nil {
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
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO submission_quarantine_tombstones (
				team_id, fragment_id, ingest_id, owner_profile_id, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)
		`, teamID, fragmentID, ingestID, profileID, "sha256:"+fragmentID)
		return err
	}))

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081701))
	var spaceID uuid.UUID
	var spaceKind string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT fragment.space_id, space.kind
		FROM evidence_fragments AS fragment
		JOIN memory_spaces AS space
		  ON space.id = fragment.space_id
		 AND space.team_id = fragment.team_id
		WHERE fragment.team_id = $1::uuid
	`, teamID).Scan(&spaceID, &spaceKind))
	assert.NotEqual(t, uuid.Nil, spaceID, "existing append-only evidence must receive the shared space")
	assert.Equal(t, "team_shared", spaceKind)
	var tombstoneSpaceID, predicateEventSpaceID uuid.UUID
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT tombstone.space_id
		FROM submission_quarantine_tombstones AS tombstone
		WHERE tombstone.team_id = $1::uuid
	`, teamID).Scan(&tombstoneSpaceID))
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT event.space_id
		FROM predicate_registration_events AS event
		WHERE event.team_id = $1::uuid
	`, teamID).Scan(&predicateEventSpaceID))
	assert.NotEqual(t, uuid.Nil, tombstoneSpaceID, "quarantine tombstone must receive the shared space")
	assert.NotEqual(t, uuid.Nil, predicateEventSpaceID, "predicate registration history must receive the shared space")
	var triggerEnabled bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT tgenabled = 'O'
		FROM pg_trigger
		WHERE tgname = 'evidence_fragments_append_only'
	`).Scan(&triggerEnabled))
	assert.True(t, triggerEnabled, "append-only guard must be restored after backfill")
}
