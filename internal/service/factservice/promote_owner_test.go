package factservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func TestPromoteOwnerScopedMutations(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("actor cannot promote foreign-owned claim", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		ownerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "web",
		})
		row := makeClaimRow("claim-foreign-owner", "Alice", "likes", "coffee", string(domain.StatusValidated))
		row["owner_profile_id"] = ownerID.String()
		row["owner_profile_name"] = "native"
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-foreign-owner")

		require.Nil(t, got)
		require.ErrorIs(t, err, ownership.ErrOwnerMismatch)
		require.Equal(t, 1, db.callCount)
		require.Zero(t, db.writeTxCount)
	})

	t.Run("foreign conflict defers instead of superseding another owner fact", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRowForSingleCurrent("claim-foreign-conflict", "Alice", "Corp X")
		claimRow["status"] = string(domain.StatusValidated)
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"
		factRow := makeFactRow("fact-foreign-weaker", "Alice", "works_at", "active", time.Now().UTC())
		factRow["object"] = "Corp Y"
		factRow["truth_score"] = 0.40
		factRow["owner_profile_id"] = foreignOwnerID.String()
		factRow["owner_profile_name"] = "web"

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {factRow},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-foreign-conflict")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrPromotionDeferredDisputed)
		require.Equal(t, 1, db.writeTxCount)
		require.Equal(t, profileID, db.lastWriteProfile)
	})

	t.Run("foreign same fact is reused without creating duplicate owner fact", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRowForSingleCurrent("claim-adopt-foreign-same", "Alice", "Acme Corp")
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"
		foreignSame := makeFactRow("fact-foreign-same", "Alice", "works_at", "active", time.Now().UTC())
		foreignSame["object"] = "Acme Corp"
		foreignSame["owner_profile_id"] = foreignOwnerID.String()
		foreignSame["owner_profile_name"] = "web"

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {foreignSame},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-adopt-foreign-same")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "fact-foreign-same", got.FactID)
		require.Equal(t, foreignOwnerID.String(), got.OwnerProfileID)
		require.Equal(t, 1, db.writeTxCount, "only the claim-to-existing-fact link should be written")
	})

	t.Run("own conflict is superseded before reusing foreign same fact", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRowForSingleCurrent("claim-adopt-after-own-conflict", "Alice", "Acme Corp")
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"
		ownConflict := makeFactRow("fact-owned-old", "Alice", "works_at", "active", time.Now().UTC())
		ownConflict["object"] = "Old Corp"
		ownConflict["truth_score"] = 0.40
		ownConflict["owner_profile_id"] = actorID.String()
		ownConflict["owner_profile_name"] = "native"
		foreignSame := makeFactRow("fact-foreign-current", "Alice", "works_at", "active", time.Now().UTC())
		foreignSame["object"] = "Acme Corp"
		foreignSame["owner_profile_id"] = foreignOwnerID.String()
		foreignSame["owner_profile_name"] = "web"

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {ownConflict, foreignSame},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-adopt-after-own-conflict")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "fact-foreign-current", got.FactID)
		require.Equal(t, foreignOwnerID.String(), got.OwnerProfileID)
		require.Equal(t, 2, db.writeTxCount, "own supersession and claim adoption should be the only writes")
	})

	t.Run("versioned keeps foreign versions and links overlays", func(t *testing.T) {
		const predicate = "employment_versioned_owner_test"
		origGate, hadGate := DefaultPromotionGates[predicate]
		defer restorePromotionGate(predicate, origGate, hadGate)
		DefaultPromotionGates[predicate] = PromotionGate{
			Policy:              Versioned,
			MinExtractConf:      0.70,
			MinResolutionConf:   0.60,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}

		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRow("claim-versioned-owner", "Alice", predicate, "Acme Corp", string(domain.StatusValidated))
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"

		ownedOld := makeFactRow("fact-owned-old", "Alice", predicate, "active", time.Now().UTC())
		ownedOld["object"] = "Old Corp"
		ownedOld["owner_profile_id"] = actorID.String()
		foreignSame := makeFactRow("fact-foreign-same", "Alice", predicate, "active", time.Now().UTC())
		foreignSame["object"] = "Acme Corp"
		foreignSame["owner_profile_id"] = foreignOwnerID.String()
		foreignDifferent := makeFactRow("fact-foreign-different", "Alice", predicate, "active", time.Now().UTC())
		foreignDifferent["object"] = "Other Corp"
		foreignDifferent["owner_profile_id"] = foreignOwnerID.String()

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {ownedOld, foreignSame, foreignDifferent},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-versioned-owner")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, actorID.String(), got.OwnerProfileID)
		require.Equal(t, "Acme Corp", got.Object)
		require.Equal(t, 4, db.writeTxCount)
	})

	t.Run("confirm accept supersedes own conflicts and overlays foreign facts", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRowForSingleCurrent("claim-confirm-owner", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusDisputed)
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"

		ownConflict := makeFactRow("fact-owned-conflict", "Alice", "works_at", "active", time.Now().UTC())
		ownConflict["object"] = "Old Corp"
		ownConflict["owner_profile_id"] = actorID.String()
		foreignConflict := makeFactRow("fact-foreign-conflict", "Alice", "works_at", "active", time.Now().UTC())
		foreignConflict["object"] = "Other Corp"
		foreignConflict["owner_profile_id"] = foreignOwnerID.String()
		foreignAlignment := makeFactRow("fact-foreign-alignment", "Alice", "works_at", "active", time.Now().UTC())
		foreignAlignment["object"] = "Acme Corp"
		foreignAlignment["owner_profile_id"] = foreignOwnerID.String()

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {ownConflict, foreignConflict, foreignAlignment},
			},
		}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		audit := &captureAuditEmitter{}
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.ConfirmMemory(actorCtx, profileID, ConfirmMemoryRequest{
			ClaimID:  "claim-confirm-owner",
			Decision: "accept_claim",
		})

		require.NoError(t, err)
		require.Equal(t, "accepted", got.Status)
		require.NotNil(t, got.Fact)
		require.Equal(t, actorID.String(), got.Fact.OwnerProfileID)
		require.Equal(t, "Acme Corp", got.Fact.Object)
		require.Equal(t, 4, db.writeTxCount)
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.confirm.accept", audit.entries[0].Operation)
	})

	t.Run("confirm accept reuses foreign same fact after superseding own conflict", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRowForSingleCurrent("claim-confirm-adopt", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusDisputed)
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"

		ownConflict := makeFactRow("fact-owned-conflict", "Alice", "works_at", "active", time.Now().UTC())
		ownConflict["object"] = "Old Corp"
		ownConflict["owner_profile_id"] = actorID.String()
		ownConflict["owner_profile_name"] = "native"
		foreignAlignment := makeFactRow("fact-foreign-current", "Alice", "works_at", "active", time.Now().UTC())
		foreignAlignment["object"] = "Acme Corp"
		foreignAlignment["owner_profile_id"] = foreignOwnerID.String()
		foreignAlignment["owner_profile_name"] = "web"

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {ownConflict, foreignAlignment},
			},
		}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		audit := &captureAuditEmitter{}
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.ConfirmMemory(actorCtx, profileID, ConfirmMemoryRequest{
			ClaimID:  "claim-confirm-adopt",
			Decision: "accept_claim",
		})

		require.NoError(t, err)
		require.Equal(t, "accepted", got.Status)
		require.NotNil(t, got.Fact)
		require.Equal(t, "fact-foreign-current", got.Fact.FactID)
		require.Equal(t, foreignOwnerID.String(), got.Fact.OwnerProfileID)
		require.Equal(t, 2, db.writeTxCount, "own supersession and claim adoption should be the only writes")
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Len(t, audit.entries, 1)
		require.Equal(t, "fact-foreign-current", audit.entries[0].EntityID)
	})
}

func restorePromotionGate(predicate string, gate PromotionGate, ok bool) {
	if ok {
		DefaultPromotionGates[predicate] = gate
	} else {
		delete(DefaultPromotionGates, predicate)
	}
}
