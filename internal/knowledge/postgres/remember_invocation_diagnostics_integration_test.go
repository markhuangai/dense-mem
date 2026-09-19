//go:build integration

package postgres

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/remember/service/processor"
)

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
	require.Equal(t, "failed", detail.Outcome)
	require.Equal(t, "assessment", detail.FailedPhase)
	require.Equal(t, "hash_only", detail.RequestCaptureState)
	require.NotContains(t, string(detail.RequestBody), "admitted-secret")
	require.NotEmpty(t, detail.ProviderExchanges)
	require.Equal(t, "provider_not_called", detail.ProviderExchanges[0].CaptureState)

	_, err = rememberProcessor.ProcessRemember(ctx, rememberapp.RememberProcessRequest{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: space.ID.String(), SpaceGeneration: spaceGeneration,
		IdempotencyKey: "processor-policy-rejection", RequestHash: "sha256:policy-rejection",
		OriginalRequest: []byte(`{"evidence":[{"content":"admitted-secret"}]}`),
	})
	require.ErrorIs(t, err, rememberapp.ErrRememberPersistence)
	replayedPage, err := repo.ListRememberInvocationDiagnostics(ctx, knowledgecontract.RememberInvocationDiagnosticFilter{
		TeamID: teamID, OwnerProfileID: ownerID, Outcome: "replayed", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, replayedPage.Records, 1)
	require.Equal(t, "replay", replayedPage.Records[0].Classification)
	require.Equal(t, page.Records[0].InvocationID, replayedPage.Records[0].CanonicalAttemptID)

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
		Classification: "execution", Outcome: "cancelled", CreatedAt: old, CompletedAt: old.Add(time.Minute), ExpiresAt: old.Add(24 * time.Hour),
	}))
	privateRepo := privacypostgres.NewPrivateMemoryRepository(appDB, rls)
	_, _, err = privateRepo.PlaceLegalHold(ctx, privateSpace.ID, "retention_case")
	require.NoError(t, err)
	heldDetail, err := repo.GetRememberInvocationDiagnostic(ctx, teamA, heldID)
	require.NoError(t, err)
	require.True(t, heldDetail.RetainedByLegalHold)
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
