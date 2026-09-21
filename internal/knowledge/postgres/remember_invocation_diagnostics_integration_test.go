//go:build integration

package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/google/uuid"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/remember/service/processor"
)

func TestRememberInvocationDiagnosticsTrimsAgainstJSONBTextLimit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-invocation-jsonb-limit-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-invocation-jsonb-limit-owner")
	// Keep the synthetic 64 MiB payload out of slow-query logs consumed by the CI output filter.
	appDB = appDB.Session(&gorm.Session{Logger: appDB.Logger.LogMode(gormlogger.Error)})
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	body := []byte(strings.Repeat("x", 16<<20))
	exchanges := make([]knowledgecontract.RememberAttemptDiagnosticInput, 4)
	for index := range exchanges {
		exchanges[index] = knowledgecontract.RememberAttemptDiagnosticInput{
			SequenceNo: index + 1, Kind: "provider_exchange", Component: "provider",
			ResponseBody: body, Outcome: "captured", CaptureState: "captured",
		}
	}
	invocationID := uuid.NewString()
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamID, OwnerProfileID: ownerID, InvocationID: invocationID,
		Classification: "execution", Outcome: "completed", ProviderExchanges: exchanges,
	}))

	var providerBytes int64
	require.NoError(t, adminDB.Raw(`
		SELECT octet_length(provider_exchanges::text)
		FROM remember_invocation_diagnostics
		WHERE team_id = ?::uuid AND invocation_id = ?::uuid
	`, teamID, invocationID).Scan(&providerBytes).Error)
	require.LessOrEqual(t, providerBytes, int64(64<<20))
	detail, err := repo.GetRememberInvocationDiagnostic(ctx, teamID, invocationID)
	require.NoError(t, err)
	require.Len(t, detail.ProviderExchanges, 4)
	require.Equal(t, "truncated", detail.ProviderExchanges[3].CaptureState)
}

func TestRememberInvocationDiagnosticsFiltersIdentityAndRetryable(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-invocation-filter-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-invocation-filter-owner")
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	canonicalID := uuid.NewString()
	retryableID := uuid.NewString()
	nonRetryableID := uuid.NewString()
	for _, input := range []knowledgecontract.RememberInvocationDiagnosticInput{
		{TeamID: teamID, OwnerProfileID: ownerID, InvocationID: canonicalID, CanonicalAttemptID: canonicalID, RequestHash: "hash-filter", CorrelationID: "corr-filter", Classification: "execution", Outcome: "failed", Retryable: true},
		{TeamID: teamID, OwnerProfileID: ownerID, InvocationID: retryableID, CanonicalAttemptID: canonicalID, RequestHash: "hash-filter", CorrelationID: "corr-filter", Classification: "replay", Outcome: "replayed", Retryable: true},
		{TeamID: teamID, OwnerProfileID: ownerID, InvocationID: nonRetryableID, RequestHash: "other-hash", CorrelationID: "other-corr", Classification: "conflict", Outcome: "conflict", Retryable: false},
	} {
		require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, input))
	}

	retryable := true
	page, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, CanonicalAttemptID: canonicalID,
		RequestHash: "hash-filter", CorrelationID: "corr-filter", Classification: "replay", Retryable: &retryable, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Equal(t, retryableID, page.Records[0].InvocationID)

	nonRetryable := false
	page, err = repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, InvocationID: nonRetryableID, Retryable: &nonRetryable, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Equal(t, nonRetryableID, page.Records[0].InvocationID)
}

func TestRememberProcessorPersistsInvocationDiagnosticsThroughPostgres(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-invocation-processor-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-invocation-processor-owner")
	space, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	spaceGeneration := privateSpaceGeneration(t, ctx, adminDB, rls, space.ID)
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	rememberProcessor := processor.NewSynchronousProcessor(processor.ProcessorDependencies{
		Ledger: repo, Logger: observability.New(slog.LevelError),
		DiagnosticProtector: observability.NewCredentialProtector("admitted-secret"),
	})

	_, err = rememberProcessor.ProcessRemember(ctx, rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		IdempotencyKey: "processor-policy-rejection", RequestHash: "sha256:policy-rejection",
		OriginalRequest: []byte(`{"evidence":[{"content":"admitted-secret"}]}`),
		Evidence:        []rememberapp.EvidenceInput{{Content: "admitted-secret"}}, SecurityRejected: true, InitialSecurityRejected: true,
	})
	require.ErrorIs(t, err, rememberapp.ErrRememberPolicyRejected)

	page, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, Outcome: "failed", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	detail, err := repo.GetRememberInvocationDiagnostic(ctx, teamID, page.Records[0].InvocationID)
	require.NoError(t, err)
	require.Equal(t, "execution", detail.Classification)
	require.Equal(t, detail.InvocationID, detail.CanonicalAttemptID)
	require.Equal(t, "failed", detail.Outcome)
	require.Equal(t, "assessment", detail.FailedPhase)
	require.Equal(t, "hash_only", detail.RequestCaptureState)
	require.NotContains(t, string(detail.RequestBody), "admitted-secret")
	require.NotEmpty(t, detail.ProviderExchanges)
	require.Equal(t, "provider_not_called", detail.ProviderExchanges[0].CaptureState)

	completedID := uuid.NewString()
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamID, OwnerProfileID: ownerID, InvocationID: completedID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		Classification: "execution", Outcome: "completed", RequestBody: []byte(`{"completed":true}`), RequestCaptureState: "captured",
		ProviderExchanges: []knowledgecontract.RememberAttemptDiagnosticInput{{
			SequenceNo: 1, Kind: "provider_exchange", Component: "verifier", Outcome: "captured", CaptureState: "captured",
		}},
	}))
	completed, err := repo.GetRememberInvocationDiagnostic(ctx, teamID, completedID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Outcome)
	require.Equal(t, []byte(`{"completed":true}`), completed.RequestBody)
	require.Equal(t, "captured", completed.RequestCaptureState)

	evaluatedZeroID := uuid.NewString()
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamID, OwnerProfileID: ownerID, InvocationID: evaluatedZeroID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		Classification: "execution", Outcome: "evaluated_zero", RequestBody: []byte(`{"relationships":[]}`), RequestCaptureState: "captured",
	}))
	evaluatedZero, err := repo.GetRememberInvocationDiagnostic(ctx, teamID, evaluatedZeroID)
	require.NoError(t, err)
	require.Equal(t, "evaluated_zero", evaluatedZero.Outcome)
	require.Equal(t, []byte(`{"relationships":[]}`), evaluatedZero.RequestBody)
	require.Equal(t, "captured", evaluatedZero.RequestCaptureState)

	_, err = rememberProcessor.ProcessRemember(ctx, rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		IdempotencyKey: "processor-policy-rejection", RequestHash: "sha256:policy-rejection",
		OriginalRequest: []byte(`{"evidence":[{"content":"admitted-secret"}]}`),
	})
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	replayedPage, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, Outcome: "failed", Limit: 10,
	})
	require.NoError(t, err)
	var replayed *knowledgecontract.RememberInvocationDiagnosticRecord
	for index := range replayedPage.Records {
		if replayedPage.Records[index].Classification == "replay" {
			replayed = &replayedPage.Records[index]
			break
		}
	}
	require.NotNil(t, replayed)
	require.Equal(t, page.Records[0].InvocationID, replayed.CanonicalAttemptID)

	_, err = rememberProcessor.ProcessRemember(ctx, rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		IdempotencyKey: "processor-policy-rejection", RequestHash: "sha256:changed",
		OriginalRequest: []byte(`{"evidence":[{"content":"changed"}]}`),
	})
	require.ErrorIs(t, err, rememberapp.ErrRememberConflict)
	conflictPage, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, Outcome: "conflict", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, conflictPage.Records, 1)
	require.Equal(t, "conflict", conflictPage.Records[0].Classification)
	require.Equal(t, page.Records[0].InvocationID, conflictPage.Records[0].CanonicalAttemptID)

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = rememberProcessor.ProcessRemember(cancelledCtx, rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		IdempotencyKey: "processor-cancelled-waiter", RequestHash: "sha256:cancelled",
		OriginalRequest: []byte(`{"evidence":[{"content":"cancelled"}]}`),
	})
	require.ErrorIs(t, err, context.Canceled)
	cancelledPage, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, Outcome: "cancelled", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, cancelledPage.Records, 1)
	require.Equal(t, "execution", cancelledPage.Records[0].Classification)
	require.Empty(t, cancelledPage.Records[0].CanonicalAttemptID)
}

func TestRememberInvocationDiagnosticsRejectsPrivateSpaceSealRace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-invocation-diagnostics-space-race")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-invocation-diagnostics-space-race-owner")
	space, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	generation := privateSpaceGeneration(t, ctx, adminDB, rls, space.ID)
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	invocationID := uuid.NewString()

	sealReady := make(chan struct{})
	releaseSeal := make(chan struct{})
	sealErr := make(chan error, 1)
	go func() {
		sealErr <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			result := tx.Exec(`
				UPDATE memory_spaces
				SET lifecycle_state = 'sealed', generation = generation + 1,
				    sealed_at = now(), updated_at = now()
				WHERE team_id = ?::uuid AND id = ?::uuid AND lifecycle_state = 'active'
			`, teamID, space.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("private diagnostic space was not sealed")
			}
			close(sealReady)
			<-releaseSeal
			return nil
		})
	}()
	select {
	case <-sealReady:
	case err := <-sealErr:
		require.NoError(t, err)
		return
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for private-space seal")
	}

	recordErr := repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamID, OwnerProfileID: ownerID, InvocationID: invocationID,
		Classification: "execution", Outcome: "failed", SpaceID: space.ID.String(), SpaceGeneration: generation,
		RequestBody: []byte(`{"private":"diagnostic"}`), RequestCaptureState: "captured",
	})
	require.Error(t, recordErr, "a sealed private space must reject a diagnostic while its erasure fence is held")
	require.NoError(t, func() error {
		close(releaseSeal)
		return <-sealErr
	}())

	_, err = repo.GetRememberInvocationDiagnostic(ctx, teamID, invocationID)
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticNotFound)

	recordErr = repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamID, OwnerProfileID: ownerID, InvocationID: uuid.NewString(),
		Classification: "execution", Outcome: "failed", SpaceID: space.ID.String(), SpaceGeneration: generation,
		RequestBody: []byte(`{"private":"diagnostic"}`), RequestCaptureState: "captured",
	})
	require.Error(t, recordErr, "a stale generation must remain rejected after the erasure fence commits")
}

func TestRememberInvocationDiagnosticsScopesBodiesAndPurgesExpiredRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "remember-invocation-diagnostics-a")
	teamB := createLedgerTeam(t, adminDB, rls, "remember-invocation-diagnostics-b")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "remember-invocation-diagnostics-owner-a")
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	privateSpace, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamA), uuid.MustParse(ownerA))
	require.NoError(t, err)
	invocationID := "00000000-0000-4000-8000-000000000801"
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamA, OwnerProfileID: ownerA, InvocationID: invocationID,
		Classification: "conflict", Outcome: "conflict", RequestHash: "sha256:changed",
		SpaceID: privateSpace.ID.String(), SpaceGeneration: 1,
		Duration:    2 * time.Second,
		RequestBody: []byte(`{"evidence":[{"content":"admitted"}]}`), RequestCaptureState: "captured",
		ProviderExchanges: []knowledgecontract.RememberAttemptDiagnosticInput{{
			SequenceNo: 1, Kind: "provider_exchange", Component: "assessor",
			RequestBody: []byte(`{"messages":[{"content":"admitted"}]}`), ResponseBody: []byte(`{"error":"database unavailable"}`),
			Outcome: "captured", CaptureState: "captured",
		}},
		CallerResponse: []byte(`{"isError":true}`), CallerResponseCaptureState: "captured",
	}))
	detail, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, invocationID)
	require.NoError(t, err)
	require.Equal(t, "conflict", detail.Classification)
	require.Equal(t, 2*time.Second, detail.Duration)
	require.Equal(t, []byte(`{"evidence":[{"content":"admitted"}]}`), detail.RequestBody)
	require.Equal(t, "captured", detail.RequestCaptureState)
	require.Contains(t, string(detail.ProviderExchanges[0].ResponseBody), "database unavailable")
	require.Equal(t, "captured", detail.CallerResponseCaptureState)
	_, err = repo.GetRememberInvocationDiagnostic(ctx, teamB, invocationID)
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticNotFound)

	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	expiredID := "00000000-0000-4000-8000-000000000802"
	heldID := "00000000-0000-4000-8000-000000000803"
	lateHeldID := "00000000-0000-4000-8000-000000000805"
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamA, OwnerProfileID: ownerA, InvocationID: heldID, SpaceID: privateSpace.ID.String(), SpaceGeneration: 1,
		Classification: "execution", Outcome: "cancelled", RequestBody: []byte(`{"held":true}`), RequestCaptureState: "captured",
		CreatedAt: old, CompletedAt: old.Add(time.Minute), ExpiresAt: old.Add(24 * time.Hour),
	}))
	privateRepo := privacypostgres.NewPrivateMemoryRepository(appDB, rls)
	_, _, err = privateRepo.PlaceLegalHold(ctx, privateSpace.ID, "retention_case")
	require.NoError(t, err)
	heldDetail, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, heldID)
	require.NoError(t, err)
	require.True(t, heldDetail.RetainedByLegalHold)
	require.Equal(t, []byte(`{"held":true}`), heldDetail.RequestBody)
	require.Equal(t, "captured", heldDetail.RequestCaptureState)
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamA, OwnerProfileID: ownerA, InvocationID: expiredID, Classification: "execution", Outcome: "cancelled",
		CreatedAt: old, CompletedAt: old.Add(time.Minute), ExpiresAt: old.Add(24 * time.Hour),
	}))
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamA, OwnerProfileID: ownerA, InvocationID: lateHeldID, SpaceID: privateSpace.ID.String(), SpaceGeneration: 1,
		Classification: "execution", Outcome: "cancelled", CreatedAt: old, CompletedAt: old.Add(time.Minute), ExpiresAt: old.Add(24 * time.Hour),
	}))
	lateHeldDetail, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, lateHeldID)
	require.NoError(t, err)
	require.True(t, lateHeldDetail.RetainedByLegalHold)
	expiredReadID := "00000000-0000-4000-8000-000000000804"
	require.NoError(t, repo.RecordRememberInvocationDiagnostic(ctx, knowledgecontract.RememberInvocationDiagnosticInput{
		TeamID: teamA, OwnerProfileID: ownerA, InvocationID: expiredReadID, Classification: "execution", Outcome: "failed",
		RequestBody: []byte(`{"expired":true}`), RequestCaptureState: "captured",
		CreatedAt: old, CompletedAt: old.Add(time.Minute), ExpiresAt: old.Add(24 * time.Hour),
	}))
	expiredDetail, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, expiredReadID)
	require.NoError(t, err)
	require.Empty(t, expiredDetail.RequestBody)
	require.Equal(t, "expired", expiredDetail.RequestCaptureState)
	deleted, err := repo.PurgeExpiredRememberAttemptDiagnostics(ctx, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, 1)
	_, err = repo.GetRememberInvocationDiagnostic(ctx, teamA, expiredID)
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticNotFound)
	_, err = repo.GetRememberInvocationDiagnostic(ctx, teamA, heldID)
	require.NoError(t, err)
	_, err = repo.GetRememberInvocationDiagnostic(ctx, teamA, lateHeldID)
	require.NoError(t, err)
	_, _, err = privateRepo.ReleaseLegalHold(ctx, privateSpace.ID)
	require.NoError(t, err)
	heldAfterRelease, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, heldID)
	require.NoError(t, err)
	require.False(t, heldAfterRelease.RetainedByLegalHold)
	require.True(t, heldAfterRelease.ExpiresAt.Before(time.Now().UTC()))
	lateHeldAfterRelease, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, lateHeldID)
	require.NoError(t, err)
	require.False(t, lateHeldAfterRelease.RetainedByLegalHold)
	require.True(t, lateHeldAfterRelease.ExpiresAt.Before(time.Now().UTC()))
	deleted, err = repo.PurgeExpiredRememberAttemptDiagnostics(ctx, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, 2)
	_, err = repo.GetRememberInvocationDiagnostic(ctx, teamA, lateHeldID)
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticNotFound)
}

func TestRememberInvocationLeavesCanonicalAttemptEmptyAfterRollback(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "canonical-failure-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "canonical-failure-owner")
	space, err := privacypostgres.NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	generation := privateSpaceGeneration(t, ctx, adminDB, rls, space.ID)
	require.NoError(t, adminDB.Exec(`
        CREATE FUNCTION reject_remember_failure_for_test() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN RAISE EXCEPTION 'injected attempt persistence failure' USING ERRCODE = '40001'; END $$;
        CREATE TRIGGER reject_remember_failure_for_test BEFORE INSERT ON remember_attempts
        FOR EACH ROW EXECUTE FUNCTION reject_remember_failure_for_test();
    `).Error)
	defer func() {
		require.NoError(t, adminDB.Exec(`DROP TRIGGER reject_remember_failure_for_test ON remember_attempts; DROP FUNCTION reject_remember_failure_for_test();`).Error)
	}()
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	rememberProcessor := processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: repo, DiagnosticProtector: observability.NewCredentialProtector()})
	_, err = rememberProcessor.ProcessRemember(ctx, rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: generation,
		IdempotencyKey: "canonical-failure", RequestHash: "sha256:canonical-failure", SecurityRejected: true, InitialSecurityRejected: true,
		Evidence:        []rememberapp.EvidenceInput{{Content: "admitted evidence"}},
		OriginalRequest: []byte(`{"evidence":[{"content":"admitted evidence"}]}`),
	})
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	var attempts int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Table("remember_attempts").Where("team_id = ?::uuid", teamID).Count(&attempts).Error
	}))
	require.Zero(t, attempts)
	page, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{TeamID: teamID, OwnerProfileID: ownerID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Empty(t, page.Records[0].CanonicalAttemptID, "a failed canonical write must not create a dangling canonical link")
}
