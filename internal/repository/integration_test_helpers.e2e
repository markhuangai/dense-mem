package repository

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	ledgerTestRole     = "densemem_rls_test"
	ledgerTestPassword = "densemem_rls_test"
)

func setupLedgerRepositoryDB(t *testing.T) (*gorm.DB, *gorm.DB, *storagepostgres.RLS, func()) {
	t.Helper()
	dsn, baseCleanup := setupLedgerRepositoryDSN(t)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storagepostgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))

	rls := storagepostgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
					CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				ELSE
					ALTER ROLE %[1]s WITH LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				END IF;
			END $$;
			GRANT USAGE ON SCHEMA public TO %[1]s;
			GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[1]s;
			GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[1]s;
			GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID, UUID) TO %[1]s;
			GRANT EXECUTE ON FUNCTION dense_mem_lock_memory_space(UUID, UUID) TO %[1]s;
		`, ledgerTestRole, ledgerTestPassword)).Error
	}))

	appDB, err := gorm.Open(gormpostgres.Open(ledgerAppDSN(t, dsn)), &gorm.Config{})
	require.NoError(t, err)
	cleanup := func() {
		_ = rls.WithSystemTx(context.Background(), db, truncateLedgerFixtures)
		if sqlDB, err := appDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
			return tx.Exec(fmt.Sprintf(`
				REASSIGN OWNED BY %[1]s TO CURRENT_USER;
				DROP OWNED BY %[1]s;
				DROP ROLE IF EXISTS %[1]s;
			`, ledgerTestRole)).Error
		})
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		baseCleanup()
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), db, truncateLedgerFixtures))
	return db, appDB, rls, cleanup
}
func setupLedgerRepositoryDSN(t *testing.T) (string, func()) {
	t.Helper()
	if dsn := storagepostgres.GetTestDSN(); dsn != "" {
		if os.Getenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS") != "1" {
			t.Skip("set DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1 to run destructive ledger PostgreSQL integration tests against DATABASE_URL")
		}
		return dsn, func() {}
	}
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 to run disposable ledger PostgreSQL integration tests")
	}

	ctx := context.Background()
	containerOptions := []testcontainers.ContainerCustomizer{
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30 * time.Second),
		),
	}
	containerOptions = append(containerOptions, precheckContainerLabels()...)
	containerOptions = append(containerOptions, precheckNetworkOptions()...)
	container, err := tcpostgres.Run(ctx, "pgvector/pgvector:0.8.2-pg18-trixie", containerOptions...)
	if err != nil {
		t.Fatalf("start Postgres test container: %v", err)
	}
	dsn, err := postgresContainerDSN(ctx, container)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("get Postgres test container DSN: %v", err)
	}
	return dsn, func() { _ = container.Terminate(ctx) }
}

func postgresContainerDSN(ctx context.Context, container *tcpostgres.PostgresContainer) (string, error) {
	host := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK"))
	if host == "" {
		return container.ConnectionString(ctx, "sslmode=disable")
	}
	connectionURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword("testuser", "testpass"),
		Host:   "postgres:5432",
		Path:   "/testdb",
	}
	query := connectionURL.Query()
	query.Set("sslmode", "disable")
	connectionURL.RawQuery = query.Encode()
	return connectionURL.String(), nil
}

func precheckContainerLabels() []testcontainers.ContainerCustomizer {
	contract := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_CONTRACT"))
	repository := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_REPOSITORY"))
	runID := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ID"))
	attempt := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_RUN_ATTEMPT"))
	project := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_PROJECT"))
	imageDigest := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_IMAGE_DIGEST"))
	if contract == "" || repository == "" || runID == "" || attempt == "" || project == "" || imageDigest == "" {
		return nil
	}
	return []testcontainers.ContainerCustomizer{testcontainers.WithLabels(map[string]string{
		"io.dense-mem.ci.contract":     contract,
		"io.dense-mem.ci.repository":   repository,
		"io.dense-mem.ci.run-id":       runID,
		"io.dense-mem.ci.run-attempt":  attempt,
		"io.dense-mem.ci.phase":        "precheck",
		"io.dense-mem.ci.scenario":     "precheck",
		"io.dense-mem.ci.image-digest": imageDigest,
		"io.dense-mem.ci.created-at":   time.Now().UTC().Format(time.RFC3339),
		"com.docker.compose.project":   project,
	})}
}

func precheckNetworkOptions() []testcontainers.ContainerCustomizer {
	networkName := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK"))
	if networkName == "" {
		return nil
	}
	return []testcontainers.ContainerCustomizer{tcnetwork.WithNetworkName([]string{"postgres"}, networkName)}
}

func ledgerAppDSN(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Skip("ledger RLS integration tests require DATABASE_URL in URL form")
	}
	parsed.User = url.UserPassword(ledgerTestRole, ledgerTestPassword)
	return parsed.String()
}

func truncateLedgerFixtures(tx *gorm.DB) error {
	return tx.Exec(`
		DO $$
		DECLARE table_name text;
		BEGIN
			FOREACH table_name IN ARRAY ARRAY[
				'private_memory_retention_runs', 'private_memory_erasure_operations',
				'private_memory_legal_holds', 'telemetry_first_disposition_backfill_state',
				'predicate_registration_events', 'v2_compatibility_markers',
				'v2_migration_operator_actions', 'v2_migration_gate_results',
				'v2_migration_exclusions', 'v2_migration_errors', 'v2_migration_checkpoints',
				'v2_migration_source_maps', 'v2_migration_corpus_items', 'v2_migration_runs',
				'recall_feedback_events', 'dream_evidence_target_attempts', 'dream_evidence_target_evaluations', 'hypothesis_evidence_derivation_sources',
				'remember_failure_artifacts', 'remember_attempt_events',
				'remember_attempts', 'semantic_assessments', 'search_documents',
				'search_index_generations', 'embedding_contracts', 'community_sources',
				'community_memberships', 'community_records', 'community_snapshot_runs',
				'hypotheses', 'dream_cycle_runs', 'review_tasks', 'relationship_cross_references',
				'entity_correction_events', 'entity_resolution_events', 'relationship_transition_events',
				'relationship_support_decision_events', 'relationship_evidence_supports',
				'evidence_conflict_events', 'evidence_conflict_positions', 'evidence_conflict_cases',
				'verification_events', 'relationship_observations', 'relationship_records',
				'value_records', 'entity_names', 'entity_records', 'evidence_exact_aliases',
				'evidence_occurrences', 'evidence_quarantines',
				'evidence_security_signals', 'evidence_security_events', 'evidence_fragments',
				'evidence_source_revisions', 'evidence_sources', 'knowledge_ingests',
				'ownership_aliases', 'membership_grants', 'credentials', 'team_memberships',
				'identity_external_links', 'actor_identities', 'teams'
			] LOOP
				IF to_regclass(table_name) IS NOT NULL THEN
					EXECUTE format('TRUNCATE TABLE %I CASCADE', table_name);
				END IF;
			END LOOP;
		END $$;
	`).Error
}

func createLedgerTeam(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamName string) string {
	t.Helper()
	teamID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, teamName).Error
	}))
	return teamID
}

func createLedgerProfile(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamID string, profileName string) string {
	t.Helper()
	profileID := uuid.NewString()
	keyPrefix := strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	require.NoError(t, NewCredentialRepository(db, rls).CreateCredential(context.Background(), &domain.Credential{
		ID: uuid.MustParse(profileID), TeamID: uuid.MustParse(teamID), Name: profileName,
		KeyHash: "hash-" + profileID, KeyPrefix: keyPrefix, KeySuffix: keyPrefix[:6], Scopes: []string{"read", "write"},
	}))
	return profileID
}
