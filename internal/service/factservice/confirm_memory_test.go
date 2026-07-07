package factservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestConfirmMemoryDecisionBranches(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("requires claim id before locking", func(t *testing.T) {
		locker := &stubClaimLocker{}
		svc := newTestService(&stubPromoteDB{}, locker, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{Decision: "keep_existing"})

		require.ErrorContains(t, err, "claim_id is required")
		require.Nil(t, got)
	})

	t.Run("keep existing rejects claim and emits audit", func(t *testing.T) {
		claimRow := makeClaimRow("claim-keep", "Alice", "likes", "coffee", string(domain.StatusDisputed))
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}
		audit := &captureAuditEmitter{}
		svc := newTestService(db, &stubClaimLocker{}, audit, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-keep", Decision: "keep_existing"})

		require.NoError(t, err)
		require.Equal(t, "claim-keep", got.ClaimID)
		require.Equal(t, "rejected", got.Status)
		require.Nil(t, got.Fact)
		require.Equal(t, profileID, db.lastWriteProfile)
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.confirm.reject", audit.entries[0].Operation)
	})

	t.Run("reject claim follows same reject path", func(t *testing.T) {
		claimRow := makeClaimRow("claim-reject", "Alice", "likes", "coffee", string(domain.StatusDisputed))
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-reject", Decision: "reject_claim"})

		require.NoError(t, err)
		require.Equal(t, "rejected", got.Status)
	})

	t.Run("rejects non-disputed claim status", func(t *testing.T) {
		claimRow := makeClaimRow("claim-validated", "Alice", "likes", "coffee", string(domain.StatusValidated))
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-validated", Decision: "accept_claim"})

		require.ErrorContains(t, err, "claim is not disputed")
		require.Nil(t, got)
		require.Zero(t, db.writeTxCount)
	})

	t.Run("accept claim creates fact when gates pass", func(t *testing.T) {
		claimRow := makeClaimRowForSingleCurrent("claim-accept", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusDisputed)
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{
			0: {claimRow},
			1: {},
		}}
		audit := &captureAuditEmitter{}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-accept", Decision: "accept_claim"})

		require.NoError(t, err)
		require.Equal(t, "accepted", got.Status)
		require.NotNil(t, got.Fact)
		require.Equal(t, "claim-accept", got.Fact.PromotedFromClaimID)
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.confirm.accept", audit.entries[0].Operation)
	})

	t.Run("accept claim reuses existing same-object fact", func(t *testing.T) {
		claimRow := makeClaimRowForSingleCurrent("claim-confirm-existing", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusDisputed)
		existing := makeFactRow("fact-existing-same", "Alice", "works_at", "active", time.Now().UTC())
		existing["object"] = "Acme Corp"
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{
			0: {claimRow},
			1: {existing},
		}}
		audit := &captureAuditEmitter{}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-confirm-existing", Decision: "accept_claim"})

		require.NoError(t, err)
		require.Equal(t, "accepted", got.Status)
		require.NotNil(t, got.Fact)
		require.Equal(t, "fact-existing-same", got.Fact.FactID)
		require.Equal(t, 2, db.writeTxCount, "same-object confirm and claim adoption should be the only writes")
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Len(t, audit.entries, 1)
		require.Equal(t, "fact-existing-same", audit.entries[0].EntityID)
	})

	t.Run("accept claim rejects unpoliced predicate and failed gates", func(t *testing.T) {
		unpoliced := makeClaimRow("claim-unpoliced", "Alice", "unknown_predicate", "value", string(domain.StatusDisputed))
		svc := newTestService(&stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {unpoliced}}}, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-unpoliced", Decision: "accept_claim"})
		require.ErrorIs(t, err, ErrPredicateNotPoliced)
		require.Nil(t, got)

		weak := makeClaimRowForSingleCurrent("claim-weak", "Alice", "Acme Corp")
		weak["status"] = string(domain.StatusDisputed)
		weak["extract_conf"] = 0.1
		svc = newTestService(&stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {weak}}}, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())
		got, err = svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-weak", Decision: "accept_claim"})
		require.ErrorIs(t, err, ErrGateRejected)
		require.Nil(t, got)
	})

	t.Run("invalid decision and lock errors are surfaced", func(t *testing.T) {
		claimRow := makeClaimRow("claim-invalid", "Alice", "likes", "coffee", string(domain.StatusDisputed))
		svc := newTestService(&stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())
		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-invalid", Decision: "unknown"})
		require.ErrorIs(t, err, ErrInvalidConfirmationDecision)
		require.Nil(t, got)

		lockErr := errors.New("lock failed")
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc = newTestService(&stubPromoteDB{}, &stubClaimLocker{lockErr: lockErr}, &captureAuditEmitter{}, metrics)
		got, err = svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-lock", Decision: "reject_claim"})
		require.ErrorIs(t, err, lockErr)
		require.Nil(t, got)
		require.Equal(t, 1, metrics.PromotionOutcomeCount("error"))
	})
}
