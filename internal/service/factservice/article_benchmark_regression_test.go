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

const articleBenchmarkProfile = "00000000-0000-0000-0000-00000000ab01"

func TestArticleBenchmark_ProductDecisionConflictRegression(t *testing.T) {
	ctx := context.Background()

	t.Run("unverified sales claim cannot override authoritative product decision", func(t *testing.T) {
		claimRow := articleProductDecisionClaimRow(
			"claim-sales-unverified",
			"project:enterprise-export",
			"sales says export API ships in Q3",
			string(domain.StatusCandidate),
		)
		claimRow["entailment_verdict"] = string(domain.VerdictInsufficient)
		authoritativeDecision := articleProductDecisionFactRow(
			"fact-authoritative-product-decision",
			"project:enterprise-export",
			"product decision: export API is deferred past Q3",
			0.99,
		)

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				2: {authoritativeDecision},
			},
		}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, metrics)

		got, err := svc.Promote(ctx, articleBenchmarkProfile, "claim-sales-unverified")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrClaimNotValidated)
		require.Equal(t, 1, db.callCount, "unverified claims must stop before idempotency or conflict reads")
		require.Empty(t, db.lastWriteProfile, "unverified claims must not reach any graph write path")
		require.Equal(t, 1, metrics.PromotionOutcomeCount("error"))
	})

	t.Run("validated sales claim conflicting with product decision is deferred for review", func(t *testing.T) {
		claimRow := articleProductDecisionClaimRow(
			"claim-sales-comparable",
			"project:enterprise-export",
			"sales says export API ships in Q3",
			string(domain.StatusValidated),
		)
		existingDecision := articleProductDecisionFactRow(
			"fact-product-decision",
			"project:enterprise-export",
			"product decision: export API is deferred past Q3",
			articleClaimTruthScore(),
		)

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {existingDecision},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, articleBenchmarkProfile, "claim-sales-comparable")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrPromotionDeferredDisputed)
		require.Equal(t, articleBenchmarkProfile, db.lastWriteProfile,
			"comparable conflict must mark only the requesting profile's claim disputed")
	})

	t.Run("stronger product decision supersedes weaker sales memory", func(t *testing.T) {
		claimRow := articleProductDecisionClaimRow(
			"claim-product-update",
			"project:enterprise-export",
			"product decision: export API is deferred past Q3",
			string(domain.StatusValidated),
		)
		weakerSalesFact := articleProductDecisionFactRow(
			"fact-sales-rumor",
			"project:enterprise-export",
			"sales says export API ships in Q3",
			0.40,
		)

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {weakerSalesFact},
			},
		}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, metrics)

		got, err := svc.Promote(ctx, articleBenchmarkProfile, "claim-product-update")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "product decision: export API is deferred past Q3", got.Object)
		require.NotEqual(t, "fact-sales-rumor", got.FactID)
		require.Equal(t, domain.FactStatusActive, got.Status)
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Equal(t, articleBenchmarkProfile, db.lastWriteProfile)
	})

	t.Run("team isolation blocks same claim id from another profile", func(t *testing.T) {
		const otherProfile = "00000000-0000-0000-0000-00000000ab02"
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, otherProfile, "claim-product-update")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrClaimNotFound)
		require.Empty(t, db.lastWriteProfile)
	})
}

func TestArticleBenchmark_ConfirmationKeepsAuthoritativeDecision(t *testing.T) {
	ctx := context.Background()
	claimRow := articleProductDecisionClaimRow(
		"claim-sales-disputed",
		"project:enterprise-export",
		"sales says export API ships in Q3",
		string(domain.StatusDisputed),
	)
	db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}
	audit := &captureAuditEmitter{}
	svc := newTestService(db, &stubClaimLocker{}, audit, observability.NewInMemoryDiscoverabilityMetrics())

	got, err := svc.ConfirmMemory(ctx, articleBenchmarkProfile, ConfirmMemoryRequest{
		ClaimID:  "claim-sales-disputed",
		Decision: "keep_existing",
	})

	require.NoError(t, err)
	require.Equal(t, "rejected", got.Status)
	require.Nil(t, got.Fact)
	require.Equal(t, articleBenchmarkProfile, db.lastWriteProfile)
	require.Len(t, audit.entries, 1)
	require.Equal(t, "claim.confirm.reject", audit.entries[0].Operation)
}

func TestArticleBenchmark_LockFailureDoesNotMutateProductDecision(t *testing.T) {
	ctx := context.Background()
	lockErr := errors.New("lock timeout")
	db := &stubPromoteDB{}
	svc := newTestService(db, &stubClaimLocker{lockErr: lockErr}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

	got, err := svc.Promote(ctx, articleBenchmarkProfile, "claim-product-update")

	require.Nil(t, got)
	require.ErrorIs(t, err, lockErr)
	require.Equal(t, 0, db.callCount)
	require.Empty(t, db.lastWriteProfile)
}

func articleProductDecisionClaimRow(claimID, subject, object, status string) map[string]any {
	row := makeClaimRow(claimID, subject, "profile_fact", object, status)
	row["extract_conf"] = 0.90
	row["resolution_conf"] = 0.80
	row["source_quality"] = 0.95
	row["supported_by"] = []any{"frag-product-decision"}
	row["classification"] = map[string]any{
		"confidentiality": "internal",
		"domain":          "product",
	}
	return row
}

func articleProductDecisionFactRow(factID, subject, object string, truthScore float64) map[string]any {
	row := makeFactRow(factID, subject, "profile_fact", string(domain.FactStatusActive), time.Now().UTC())
	row["object"] = object
	row["truth_score"] = truthScore
	row["source_quality"] = 0.95
	return row
}

func articleClaimTruthScore() float64 {
	gate := DefaultPromotionGates["profile_fact"]
	claim := &domain.Claim{
		ExtractConf:                  0.90,
		ResolutionConf:               0.80,
		SourceQuality:                0.95,
		SupportedBy:                  []string{"frag-product-decision"},
		EntailmentVerdict:            domain.VerdictEntailed,
		Modality:                     domain.ModalityAssertion,
		Classification:               map[string]any{"domain": "product"},
		ProfileID:                    articleBenchmarkProfile,
		ClassificationLatticeVersion: "v1",
	}
	return ClaimStrength(claim, gate).TruthScore
}
