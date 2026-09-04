//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	evidenceConflictMigrationBase    int64 = 20260903010001
	evidenceConflictMigrationVersion int64 = 20260904010001
)

func TestEvidenceConflictMigrationEnforcesLedgerAndRollbackBoundary(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, evidenceConflictMigrationBase)
	runGooseUpTo(t, ctx, db, evidenceConflictMigrationVersion)

	for _, table := range []string{"evidence_conflict_cases", "evidence_conflict_positions", "evidence_conflict_events"} {
		require.True(t, tableExists(t, ctx, db, table), table)
	}
	for _, column := range []struct{ table, name string }{
		{"evidence_conflict_cases", "case_key"},
		{"evidence_conflict_cases", "preferred_position_id"},
		{"evidence_conflict_positions", "occurrence_id"},
		{"evidence_conflict_positions", "submitted"},
		{"evidence_conflict_events", "citation_snapshot"},
	} {
		require.True(t, columnExists(t, ctx, db, column.table, column.name), column.table+"."+column.name)
	}
	for _, index := range []string{
		"evidence_conflict_cases_activity_idx",
		"evidence_conflict_cases_space_idx",
		"evidence_conflict_positions_evidence_idx",
		"evidence_conflict_events_history_idx",
	} {
		require.True(t, indexExists(t, ctx, db, index), index)
	}
	var policyCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_policies
		WHERE schemaname = 'public'
		  AND tablename IN ('evidence_conflict_cases', 'evidence_conflict_positions', 'evidence_conflict_events')
	`).Scan(&policyCount))
	require.Equal(t, 10, policyCount)

	teamID, profileID := insertMigrationTeamProfile(t, ctx, db)
	var spaceID string
	var generation int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT id::text, generation
		FROM memory_spaces
		WHERE team_id = $1::uuid AND kind = 'team_shared'
		LIMIT 1
	`, teamID).Scan(&spaceID, &generation))
	fragmentID, ingestID := uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, status, proposal, metadata
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
			          'evidence-conflict-migration-ingest', 'evidence-conflict-migration-hash',
			          'completed', '{}'::jsonb, '{}'::jsonb)
		`, teamID, ingestID, profileID, spaceID, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id,
				space_id, space_generation, evidence_index, content, content_hash,
				source_type, authority, source_ref, labels, metadata
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 0,
			          'migration fixture evidence', 'sha256:evidence-conflict-migration',
			          'manual', 'primary', '', ARRAY[]::text[], '{}'::jsonb)
		`, teamID, fragmentID, ingestID, profileID, spaceID, generation); err != nil {
			return err
		}
		return nil
	}))

	conflictID, firstPositionID, secondPositionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_conflict_cases (
				team_id, conflict_id, space_id, space_generation, case_key, status, version
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'migration-case-key', 'open', 1)
		`, teamID, conflictID, spaceID, generation); err != nil {
			return err
		}
		for _, position := range []struct {
			id, key, quote string
			submitted      bool
		}{
			{firstPositionID, "migration-position-one", "first", true},
			{secondPositionID, "migration-position-two", "second", false},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_conflict_positions (
					team_id, conflict_id, space_id, space_generation, position_id, position_key,
					canonical_evidence_id, canonical_owner_profile_id, occurrence_id,
					occurrence_owner_profile_id, quote, span_start, span_end, authority, submitted
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6,
				          $7::uuid, $8::uuid, $7::uuid, $8::uuid, $9, 0, 1, 'primary', $10)
			`, teamID, conflictID, spaceID, generation, position.id, position.key, fragmentID, profileID, position.quote, position.submitted); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_conflict_events (
				team_id, conflict_event_id, conflict_id, space_id, space_generation, ordinal,
				action, status_after, case_version, actor_kind, actor_id, citation_snapshot
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 1,
			          'opened', 'open', 1, 'profile', $6, '[]'::jsonb)
		`, teamID, uuid.NewString(), conflictID, spaceID, generation, profileID)
		return err
	}))

	appendOnlyErr := execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE evidence_conflict_positions
			SET quote = 'rewritten'
			WHERE team_id = $1::uuid AND conflict_id = $2::uuid AND position_id = $3::uuid
		`, teamID, conflictID, firstPositionID)
		return err
	})
	require.Error(t, appendOnlyErr)
	preferredMembershipErr := execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE evidence_conflict_cases
			SET status = 'resolved', version = 2, preferred_position_id = $1::uuid, resolved_at = now()
			WHERE team_id = $2::uuid AND conflict_id = $3::uuid
		`, uuid.NewString(), teamID, conflictID)
		return err
	})
	require.Error(t, preferredMembershipErr)
	require.Error(t, migrationDownTo(ctx, db, evidenceConflictMigrationBase))
	require.True(t, tableExists(t, ctx, db, "evidence_conflict_cases"))
}
