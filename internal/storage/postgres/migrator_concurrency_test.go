//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestMigratorRunUpSerializesConcurrentStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()
	if os.Getenv("DATABASE_URL") != "" {
		isolatedDSN, isolatedCleanup := createMigrationTestDatabase(t, ctx, dsn)
		dsn = isolatedDSN
		defer isolatedCleanup()
	}

	dbs := make([]*sql.DB, 2)
	for i := range dbs {
		db, err := Open(ctx, &testConfig{dsn: dsn})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		dbs[i] = sqlDB
		defer sqlDB.Close()
	}

	start := make(chan struct{})
	errs := make(chan error, len(dbs))
	var ready sync.WaitGroup
	ready.Add(len(dbs))
	for _, db := range dbs {
		go func(sqlDB *sql.DB) {
			ready.Done()
			<-start
			errs <- NewMigratorWithDB(sqlDB).RunUp(ctx)
		}(db)
	}
	ready.Wait()
	close(start)
	for range dbs {
		require.NoError(t, <-errs)
	}

	var duplicateVersions int
	require.NoError(t, dbs[0].QueryRowContext(ctx, `
		SELECT count(*)
		FROM (
			SELECT version_id
			FROM goose_db_version
			WHERE is_applied
			GROUP BY version_id
			HAVING count(*) > 1
		) AS duplicates
	`).Scan(&duplicateVersions))
	require.Zero(t, duplicateVersions)
}

func TestMigratorRunUpAsRuntimeRoleWithoutCreateRole(t *testing.T) {
	if os.Getenv("DATABASE_URL") != "" {
		t.Skip("requires a disposable PostgreSQL instance where the test can provision roles")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	adminDB, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	adminSQLDB, err := adminDB.DB()
	require.NoError(t, err)
	defer adminSQLDB.Close()

	_, err = adminSQLDB.ExecContext(ctx, `
		CREATE EXTENSION IF NOT EXISTS pgcrypto;
		CREATE EXTENSION IF NOT EXISTS vector;
		CREATE ROLE dense_mem_migration_database_owner
			NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
		CREATE ROLE dense_mem_migration_runtime
			LOGIN PASSWORD 'dense_mem_migration_runtime'
			NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
		DO $$
		BEGIN
			EXECUTE format(
				'ALTER DATABASE %I OWNER TO dense_mem_migration_database_owner',
				current_database()
			);
			EXECUTE format(
				'GRANT CONNECT, CREATE, TEMPORARY ON DATABASE %I TO dense_mem_migration_runtime',
				current_database()
			);
		END
		$$;
		GRANT USAGE, CREATE ON SCHEMA public TO dense_mem_migration_runtime;
	`)
	require.NoError(t, err)

	runtimeConfig, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)
	runtimeConfig.User = "dense_mem_migration_runtime"
	runtimeConfig.Password = "dense_mem_migration_runtime"
	runtimeDB, err := Open(ctx, &testConfig{dsn: migrationConnInfo(runtimeConfig)})
	require.NoError(t, err)
	sqlDB, err := runtimeDB.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	var isSuperuser, canCreateDatabase, canCreateRole, canReplicate, bypassesRLS bool
	var ownsDatabase, canCreateInDatabase, canCreateInSchema bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT
			role.rolsuper,
			role.rolcreatedb,
			role.rolcreaterole,
			role.rolreplication,
			role.rolbypassrls,
			pg_get_userbyid(database.datdba) = CURRENT_USER,
			has_database_privilege(CURRENT_USER, current_database(), 'CREATE'),
			has_schema_privilege(CURRENT_USER, 'public', 'CREATE')
		FROM pg_roles AS role
		JOIN pg_database AS database ON database.datname = current_database()
		WHERE role.rolname = CURRENT_USER
	`).Scan(
		&isSuperuser,
		&canCreateDatabase,
		&canCreateRole,
		&canReplicate,
		&bypassesRLS,
		&ownsDatabase,
		&canCreateInDatabase,
		&canCreateInSchema,
	))
	require.False(t, isSuperuser)
	require.False(t, canCreateDatabase)
	require.False(t, canCreateRole)
	require.False(t, canReplicate)
	require.False(t, bypassesRLS)
	require.False(t, ownsDatabase)
	require.True(t, canCreateInDatabase)
	require.True(t, canCreateInSchema)

	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026080803))
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	contractID := uuid.NewString()
	legacyFailure := legacyEmbeddingFailureFixture{
		jobID: uuid.NewString(), documentID: uuid.NewString(), sourceID: uuid.NewString(),
		status: "failed", errorMessage: "embedding provider http error: status=429 code=insufficient_quota", attempts: 20,
	}
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model,
				dimensions, distance_metric, vector_normalization,
				document_format_version, query_format_version, lifecycle_state
			) VALUES ($1::uuid, $2, 1, 'openai', 'migration-role-model', 3, 'cosine', 'provider', 1, 1, 'active')
		`, contractID, "migration-role-reconciliation-"+contractID); err != nil {
			return err
		}
		return insertLegacyEmbeddingFailureFixture(ctx, tx, teamID, profileID, contractID, legacyFailure)
	}))
	insertMigrationAuthorityFixture(t, ctx, sqlDB, teamID, profileID, "primary")

	// Validate the embedding-reconciliation backfill while its historical table
	// still exists; the v2.6.1 hard cutover intentionally drops embedding_jobs.
	require.NoError(t, migrationUpTo(ctx, sqlDB, 2026081602))
	cutoverIngestID := uuid.NewString()
	cutoverRunID := uuid.NewString()
	cutoverAssessmentID := uuid.NewString()
	cutoverResolutionID := uuid.NewString()
	cutoverObservationID := uuid.NewString()
	cutoverVerificationID := uuid.NewString()
	cutoverReviewTaskID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'runtime-role-cutover',
				'runtime-role-cutover-request', 'completed', '{}'::jsonb,
				jsonb_build_object(
					'contract_version', 'dense-mem.v2.6',
					'actor', jsonb_build_object(
						'team_id', $1::uuid,
						'profile_id', $3::uuid
					)
				),
				now()
			)
		`, teamID, cutoverIngestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, attempts, max_attempts, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'completed', 1, 5, now())
		`, teamID, cutoverRunID, cutoverIngestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
				team_id, assessment_id, owner_profile_id, request_id,
				assessor_contract_version, model, tokenizer,
				input_tokens, output_tokens, candidate_context_tokens,
				normalized_response, response_hash, validated_at,
				assessment_scope, placement_run_id, ingest_id
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'runtime-role-cutover-assessment',
				'dense-mem.v2.6', 'fixture-model', 'fixture-tokenizer',
				10, 5, 3, '{"accepted":true}'::jsonb,
				'sha256:runtime-role-cutover-assessment', now(),
				'submission', $4::uuid, $5::uuid
			)
		`, teamID, cutoverAssessmentID, profileID, cutoverRunID, cutoverIngestID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_resolution_events (
				team_id, resolution_event_id, ingest_id, owner_profile_id,
				mention_ref, action, assessment_id
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'runtime-role-ambiguous-entity', 'ambiguous', $5::uuid
			)
		`, teamID, cutoverResolutionID, cutoverIngestID, profileID, cutoverAssessmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_observations (
				team_id, observation_id, ingest_id, owner_profile_id,
				subject_ref, original_predicate, object_ref
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'runtime-role-subject', 'runtime_role_predicate', 'runtime-role-object'
			)
		`, teamID, cutoverObservationID, cutoverIngestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO verification_events (
				team_id, verification_event_id, observation_id, owner_profile_id,
				evidence_verdict, assessment_id
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid, 'entailed', $5::uuid
			)
		`, teamID, cutoverVerificationID, cutoverObservationID, profileID, cutoverAssessmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_tasks (
				team_id, review_task_id, owner_profile_id, ingest_id,
				task_type, status, reason, payload, dedupe_key, assessment_id
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'identity_needs_review', 'canceled', 'runtime_role_cutover',
				'{}'::jsonb, $5, $6::uuid
			)
		`, teamID, cutoverReviewTaskID, profileID, cutoverIngestID,
			"runtime-role-cutover:"+cutoverReviewTaskID, cutoverAssessmentID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO predicate_registration_events (
				team_id, placement_run_id, assessment_id, owner_profile_id,
				relationship_ref, registration_action, predicate_key, predicate_version
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'runtime-role-cutover-ref', 'created', 'runtime_role_predicate', 1
			)
		`, teamID, cutoverRunID, cutoverAssessmentID, profileID)
		return err
	}))
	assertMigratedEmbeddingJob(t, ctx, sqlDB, legacyFailure.jobID, migratedEmbeddingJobExpectation{
		status: "failed", totalAttempts: 20,
		failureClass: "provider_action_required", failureCode: "provider_quota_exhausted", failedTimestamps: true,
	})

	require.NoError(t, NewMigratorWithDB(sqlDB).RunUp(ctx))
	var semanticAssessmentID string
	var migratedOrigin string
	var predicateIngestID, predicateAssessmentID string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		require.NoError(t, tx.QueryRowContext(ctx, `
			SELECT metadata ->> '_dense_mem_telemetry_origin'
			FROM knowledge_ingests
			WHERE team_id = $1::uuid AND ingest_id = $2::uuid
		`, teamID, cutoverIngestID).Scan(&migratedOrigin))
		require.NoError(t, tx.QueryRowContext(ctx, `
			SELECT semantic_assessment_id::text
			FROM semantic_assessments
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, cutoverIngestID).Scan(&semanticAssessmentID))
		assessmentReferences := []struct {
			name  string
			query string
			rowID string
		}{
			{
				name:  "entity resolution",
				query: `SELECT assessment_id::text FROM entity_resolution_events WHERE team_id = $1::uuid AND resolution_event_id = $2::uuid`,
				rowID: cutoverResolutionID,
			},
			{
				name:  "verification",
				query: `SELECT assessment_id::text FROM verification_events WHERE team_id = $1::uuid AND verification_event_id = $2::uuid`,
				rowID: cutoverVerificationID,
			},
			{
				name:  "review task",
				query: `SELECT assessment_id::text FROM review_tasks WHERE team_id = $1::uuid AND review_task_id = $2::uuid`,
				rowID: cutoverReviewTaskID,
			},
		}
		for _, reference := range assessmentReferences {
			var mappedAssessmentID string
			require.NoError(t, tx.QueryRowContext(ctx, reference.query, teamID, reference.rowID).Scan(&mappedAssessmentID), reference.name)
			require.Equal(t, semanticAssessmentID, mappedAssessmentID, reference.name)
		}
		require.NoError(t, tx.QueryRowContext(ctx, `
			SELECT ingest_id::text, assessment_id::text
			FROM predicate_registration_events
			WHERE team_id = $1::uuid AND relationship_ref = 'runtime-role-cutover-ref'
		`, teamID).Scan(&predicateIngestID, &predicateAssessmentID))
		return nil
	}))
	require.Equal(t, "remember", migratedOrigin)
	require.Equal(t, cutoverIngestID, predicateIngestID)
	require.Equal(t, semanticAssessmentID, predicateAssessmentID)
	var revisionCount, sharedRevisionCount int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT
				count(*),
				count(*) FILTER (WHERE space.kind = 'team_shared')
			FROM evidence_source_revisions AS revision
			LEFT JOIN memory_spaces AS space
			  ON space.team_id = revision.team_id
			 AND space.id = revision.space_id
			WHERE revision.team_id = $1::uuid
		`, teamID).Scan(&revisionCount, &sharedRevisionCount)
	}))
	require.Equal(t, 1, revisionCount)
	require.Equal(t, revisionCount, sharedRevisionCount)
	var temporaryBackfillPolicyCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_policy
		WHERE polname IN (
			'dense_mem_2026081701_backfill_select',
			'dense_mem_2026081701_backfill_update',
			'dense_mem_20260825010001_predicate_registration_update',
			'dense_mem_20260825010001_entity_resolution_update',
			'dense_mem_20260825010001_verification_update'
		)
	`).Scan(&temporaryBackfillPolicyCount))
	require.Zero(t, temporaryBackfillPolicyCount)
	var restoredAppendOnlyTriggers int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_trigger
		WHERE NOT tgisinternal
		  AND tgname IN (
			'predicate_registration_events_append_only',
			'entity_resolution_events_append_only',
			'verification_events_append_only'
		  )
	`).Scan(&restoredAppendOnlyTriggers))
	require.Equal(t, 3, restoredAppendOnlyTriggers)
	require.False(t, tableExists(t, ctx, sqlDB, "embedding_jobs"))

	var cleanupApplied, temporaryCleanupPolicyExists bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM goose_db_version
				WHERE version_id = 2026081602 AND is_applied
			),
			EXISTS (
				SELECT 1 FROM pg_policy
				WHERE polrelid = 'teams'::regclass
				  AND polname = 'teams_v25_cleanup_migration_read'
			)
	`).Scan(&cleanupApplied, &temporaryCleanupPolicyExists))
	require.True(t, cleanupApplied)
	require.False(t, temporaryCleanupPolicyExists)

	var migrationApplied bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM goose_db_version
			WHERE version_id = 2026080603
			  AND is_applied
		)
	`).Scan(&migrationApplied))
	require.True(t, migrationApplied)

	var dedicatedRoleExists bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_roles
			WHERE rolname = 'dense_mem_portal_session_system'
		)
	`).Scan(&dedicatedRoleExists))
	require.False(t, dedicatedRoleExists)

	var portalSessionFunctionCount int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_proc AS procedure
		JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
		WHERE namespace.nspname = 'public'
		  AND procedure.proname IN (
			'dense_mem_portal_session_create',
			'dense_mem_portal_session_get',
			'dense_mem_portal_session_delete',
			'dense_mem_portal_session_delete_expired'
		  )
	`).Scan(&portalSessionFunctionCount))
	require.Zero(t, portalSessionFunctionCount)

	var rowSecurityEnabled, forcesRowSecurity, ownsPortalSessionTable bool
	var appliesToAllCommands, appliesToPublic bool
	var usingExpression, checkExpression string
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT
			class.relrowsecurity,
			class.relforcerowsecurity,
			pg_get_userbyid(class.relowner) = CURRENT_USER,
			policy.polcmd = '*',
			policy.polroles = ARRAY[0::oid],
			pg_get_expr(policy.polqual, policy.polrelid),
			pg_get_expr(policy.polwithcheck, policy.polrelid)
		FROM pg_class AS class
		JOIN pg_policy AS policy ON policy.polrelid = class.oid
		WHERE class.oid = to_regclass('public.user_portal_sessions')
		  AND policy.polname = 'user_portal_sessions_system_access'
	`).Scan(
		&rowSecurityEnabled,
		&forcesRowSecurity,
		&ownsPortalSessionTable,
		&appliesToAllCommands,
		&appliesToPublic,
		&usingExpression,
		&checkExpression,
	))
	require.True(t, rowSecurityEnabled)
	require.True(t, forcesRowSecurity)
	require.True(t, ownsPortalSessionTable)
	require.True(t, appliesToAllCommands)
	require.True(t, appliesToPublic)
	for _, expression := range []string{usingExpression, checkExpression} {
		require.Contains(t, expression, "current_setting")
		require.Contains(t, expression, "app.tx_mode")
		require.Contains(t, expression, "system")
	}

	var unownedApplicationRelations int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_class AS class
		JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
		WHERE namespace.nspname = 'public'
		  AND class.relkind IN ('r', 'p', 'S', 'v', 'm')
		  AND pg_get_userbyid(class.relowner) <> CURRENT_USER
	`).Scan(&unownedApplicationRelations))
	require.Zero(t, unownedApplicationRelations)
}
