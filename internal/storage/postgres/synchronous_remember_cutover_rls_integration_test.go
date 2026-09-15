//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestRememberSynchronousCutoverEnforcesForceRLSForRuntimeRole(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileA := insertRememberReliabilityIdentityFixture(t, ctx, db)
	profileB, identityB := uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES ($1::uuid, 'human', $2::uuid, 'Cutover FORCE RLS profile B')
		`, identityB, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, reason)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'cutover_force_rls')
		`, teamID, profileB, identityB)
		return err
	}))

	runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)

	roleName := "dense_mem_cutover_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := db.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("cutover FORCE RLS behavior test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() {
		_, _ = db.ExecContext(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = db.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole)
	}()
	for _, grant := range []string{
		"GRANT USAGE ON SCHEMA public TO " + quotedRole,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON knowledge_ingests, remember_attempts, remember_attempt_events, remember_failure_artifacts, semantic_assessments TO " + quotedRole,
		"GRANT SELECT ON ownership_aliases, memory_spaces TO " + quotedRole,
	} {
		_, err := db.ExecContext(ctx, grant)
		require.NoError(t, err)
	}

	spaceID := uuid.NewString()
	attemptA, attemptB := uuid.NewString(), uuid.NewString()
	eventA, artifactA, assessmentA := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memory_spaces (id, team_id, kind, owner_profile_id)
			VALUES ($1::uuid, $2::uuid, 'profile_private', $3::uuid)
		`, spaceID, teamID, profileA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, metadata, space_id, space_generation, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'force-rls-private',
			          'force-rls-private-request', 'completed',
			          '{"_dense_mem_telemetry_origin":"remember"}'::jsonb,
			          $4::uuid, 1, now())
		`, teamID, attemptA, profileA, spaceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, contract_version, submission_kind, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1,
			          'force-rls-private', 'force-rls-private-request', 'dense-mem.v2.6.1', 'remember', 'completed')
		`, teamID, attemptA, profileA, spaceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id,
				idempotency_key, request_hash, contract_version, submission_kind, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid,
			          'force-rls-other', 'force-rls-other-request', 'dense-mem.v2.6.1', 'remember', 'failed')
		`, teamID, attemptB, profileB); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempt_events (
				team_id, event_id, attempt_id, owner_profile_id,
				sequence_no, phase, event_kind
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          1, 'commit', 'force-rls')
		`, teamID, eventA, attemptA, profileA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_failure_artifacts (
				team_id, artifact_id, attempt_id, owner_profile_id,
				artifact_kind, content_type, content_bytes, byte_count,
				content_sha256, expires_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'force-rls', 'text/plain', decode('61', 'hex'), 1,
			          'sha256:ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb',
			          now() + interval '1 hour')
		`, teamID, artifactA, attemptA, profileA); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id,
				response_history, provider_turns, model
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, '[]'::jsonb, 1, 'force-rls')
		`, teamID, assessmentA, attemptA, profileA)
		return err
	}))

	withRole := func(txMode, currentTeamID, currentProfileID, privateSpace string, fn func(*sql.Tx) error) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
			return err
		}
		for key, value := range map[string]string{
			"app.tx_mode": txMode, "app.current_team_id": currentTeamID,
			"app.current_profile_id": currentProfileID, "app.private_erasure_space_id": privateSpace,
		} {
			if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, key, value); err != nil {
				return err
			}
		}
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	}

	require.NoError(t, withRole("profile", teamID, profileA, "", func(tx *sql.Tx) error {
		var visible, hidden int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM remember_attempts WHERE team_id = $1::uuid`, teamID).Scan(&visible); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`, teamID, attemptB).Scan(&hidden); err != nil {
			return err
		}
		if visible != 1 || hidden != 0 {
			return fmt.Errorf("FORCE RLS profile visibility = %d/%d, want 1/0", visible, hidden)
		}
		if _, err := tx.ExecContext(ctx, `SAVEPOINT force_rls_denied_insert`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id,
				idempotency_key, request_hash, contract_version, submission_kind, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid,
			          'force-rls-denied', 'force-rls-denied-request', 'dense-mem.v2.6.1', 'remember', 'failed')
		`, teamID, uuid.New(), profileB)
		if err == nil {
			return errors.New("FORCE RLS allowed a cross-profile insert")
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			return fmt.Errorf("expected cross-profile row-level-security denial, got %w", err)
		}
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT force_rls_denied_insert`); err != nil {
			return err
		}
		return nil
	}))

	require.NoError(t, withRole("system", teamID, "", spaceID, func(tx *sql.Tx) error {
		for _, statement := range []string{
			`DELETE FROM semantic_assessments WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
			`DELETE FROM remember_attempt_events WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
			`DELETE FROM remember_failure_artifacts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
			`DELETE FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
			`DELETE FROM knowledge_ingests WHERE team_id = $1::uuid AND ingest_id = $2::uuid`,
		} {
			if _, err := tx.ExecContext(ctx, statement, teamID, attemptA); err != nil {
				return err
			}
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM remember_attempts
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, attemptA).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			return errors.New("private-space erasure left the terminal attempt")
		}
		return nil
	}))
}
