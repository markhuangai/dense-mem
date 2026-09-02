package serverapp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	rememberProcessorIntegrationRole     = "densemem_processor_test"
	rememberProcessorIntegrationPassword = "densemem_processor_test"
)

func TestRememberServiceRejectsHistoricalOutcomesThroughPostgres(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.New()
	ownerID := uuid.New()
	teamName := "remember historical outcomes " + uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, teamName).Error
	}))
	credential := &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "remember historical outcomes owner",
		KeyHash: "remember-historical-outcomes-hash", KeyPrefix: strings.ReplaceAll(teamID.String(), "-", "")[:24],
		KeySuffix: "owner", Scopes: []string{"read", "write"},
	}
	require.NoError(t, repository.NewCredentialRepository(adminDB, rls).CreateCredential(ctx, credential))

	ledger := repository.NewLedgerRepository(appDB, rls)
	processor := newRememberSynchronousProcessor(
		ledger, nil, nil, nil, assessor.DefaultSemanticAssessmentLimits(),
		observability.NoopDiscoverabilityMetrics(), nil,
	)
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: ownerID, Role: "member", AuthMethod: "api_key",
		Grants: []string{"read", "write"},
	})

	for _, outcome := range []string{"rejected", "quarantined", "replayed"} {
		t.Run(outcome, func(t *testing.T) {
			key := "historical-" + outcome + "-" + uuid.NewString()
			evidence := []rememberapp.RememberEvidenceInput{{Content: "A retained historical Remember result."}}
			req := rememberapp.RememberRequest{Evidence: evidence, IdempotencyKey: key}
			hash, err := rememberapp.CanonicalRequestBodyHash(evidence, nil, nil)
			require.NoError(t, err)
			attemptID := uuid.New()
			insertHistoricalRememberOutcome(t, ctx, adminDB, appDB, rls, teamID, ownerID, attemptID, key, hash, outcome)

			result, err := service.Remember(actorCtx, req)
			require.Nil(t, result)
			var processErr *rememberapp.RememberProcessError
			require.ErrorAs(t, err, &processErr)
			require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
			require.NotNil(t, processErr.Status)
			require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
			require.Equal(t, "failed", processErr.Status.ProcessingState)
		})
	}
}

func TestRememberServiceRejectsMigratedAttemptThroughPostgres(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.New()
	ownerID := uuid.New()
	teamName := "remember migrated conflict " + uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, teamName).Error
	}))
	credential := &domain.Credential{
		ID: ownerID, TeamID: teamID, Name: "remember migrated conflict owner",
		KeyHash: "remember-migrated-conflict-hash", KeyPrefix: strings.ReplaceAll(teamID.String(), "-", "")[:24],
		KeySuffix: "owner", Scopes: []string{"read", "write"},
	}
	require.NoError(t, repository.NewCredentialRepository(adminDB, rls).CreateCredential(ctx, credential))

	ledger := repository.NewLedgerRepository(appDB, rls)
	evidence := []rememberapp.RememberEvidenceInput{{Content: "A migrated Remember attempt."}}
	key := "migrated-conflict-" + uuid.NewString()
	requestHash, err := rememberapp.CanonicalRequestBodyHash(evidence, nil, nil)
	require.NoError(t, err)
	attemptID := uuid.New()
	require.NoError(t, ledger.RecordRememberAttempt(ctx, repository.RememberAttemptRecordInput{
		TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: attemptID.String(),
		IdempotencyKey: key, RequestHash: requestHash, ContractVersion: "remember_request_hash_v1",
		SubmissionKind: "remember", Outcome: "completed", PublicResult: map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": attemptID.String(),
			"submission_kind": "remember", "processing_state": "completed", "search_state": "current",
			"correlation_id": attemptID.String(), "evidence": []any{map[string]any{
				"disposition": "stored", "evidence_id": uuid.NewString(), "evidence_index": 0,
				"superseded_evidence_ids": []any{}, "search_state": "current",
			}}, "relationship_results": []any{}, "errors": []any{},
		},
	}))

	processor := newRememberSynchronousProcessor(
		ledger, nil, nil, nil, assessor.DefaultSemanticAssessmentLimits(),
		observability.NoopDiscoverabilityMetrics(), nil,
	)
	service := rememberapp.NewService(rememberapp.Dependencies{Synchronous: processor})
	actorCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: ownerID, Role: "member", AuthMethod: "api_key",
		Grants: []string{"read", "write"},
	})

	result, err := service.Remember(actorCtx, rememberapp.RememberRequest{Evidence: evidence, IdempotencyKey: key})
	require.Nil(t, result)
	var processErr *rememberapp.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
	require.Equal(t, string(rememberapp.SubmissionErrorIdempotencyConflict), processErr.Status.Errors[0].Code)
	require.Equal(t, "failed", processErr.Status.ProcessingState)

	var totalCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		`, teamID, ownerID, key).Scan(&totalCount).Error
	}))
	require.Equal(t, 1, totalCount)
}

func insertHistoricalRememberOutcome(
	t *testing.T,
	ctx context.Context,
	adminDB, appDB *gorm.DB,
	rls *storagepostgres.RLS,
	teamID, ownerID, attemptID uuid.UUID,
	key, requestHash, outcome string,
) {
	t.Helper()
	ledger := repository.NewLedgerRepository(appDB, rls)
	if outcome != "replayed" {
		require.NoError(t, ledger.RecordRememberAttempt(ctx, repository.RememberAttemptRecordInput{
			TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: attemptID.String(),
			IdempotencyKey: key, RequestHash: requestHash, ContractVersion: domain.ContractVersion,
			SubmissionKind: "remember", Outcome: outcome, ErrorCode: "historical_" + outcome,
			PublicResult: map[string]any{"processing_state": "failed"},
		}))
		return
	}

	canonicalID := uuid.New()
	require.NoError(t, ledger.RecordRememberAttempt(ctx, repository.RememberAttemptRecordInput{
		TeamID: teamID.String(), OwnerProfileID: ownerID.String(), AttemptID: canonicalID.String(),
		IdempotencyKey: key, RequestHash: requestHash, ContractVersion: domain.ContractVersion,
		SubmissionKind: "remember", Outcome: "completed",
		PublicResult: map[string]any{"processing_state": "completed"},
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				contract_version, submission_kind, outcome, canonical_attempt_id, public_result, completed_at
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, ?, 'remember', 'replayed', ?::uuid, '{}'::jsonb, now())
		`, teamID, attemptID, ownerID, key, requestHash, domain.ContractVersion, canonicalID).Error
	}))
}

func setupRememberProcessorIntegrationDB(t *testing.T) (*gorm.DB, *gorm.DB, *storagepostgres.RLS, func()) {
	t.Helper()
	if os.Getenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS") != "1" {
		t.Skip("set DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1 to run PostgreSQL integration tests")
	}
	dsn := strings.TrimSpace(storagepostgres.GetTestDSN())
	if dsn == "" {
		t.Skip("DATABASE_URL is required for Remember processor PostgreSQL integration tests")
	}
	adminDB, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storagepostgres.NewMigrator(adminDB)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))
	rls := storagepostgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
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
		`, rememberProcessorIntegrationRole, rememberProcessorIntegrationPassword)).Error
	}))
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	if parsed.Scheme == "" || parsed.Host == "" {
		t.Skip("Remember processor integration tests require DATABASE_URL in URL form")
	}
	parsed.User = url.UserPassword(rememberProcessorIntegrationRole, rememberProcessorIntegrationPassword)
	appDB, err := gorm.Open(gormpostgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	cleanup := func() {
		if sqlDB, err := appDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			return tx.Exec(fmt.Sprintf(`
				REASSIGN OWNED BY %[1]s TO CURRENT_USER;
				DROP OWNED BY %[1]s;
				DROP ROLE IF EXISTS %[1]s;
			`, rememberProcessorIntegrationRole)).Error
		})
		if sqlDB, err := adminDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	return adminDB, appDB, rls, cleanup
}
