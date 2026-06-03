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
}

func restorePromotionGate(predicate string, gate PromotionGate, ok bool) {
	if ok {
		DefaultPromotionGates[predicate] = gate
	} else {
		delete(DefaultPromotionGates, predicate)
	}
}
