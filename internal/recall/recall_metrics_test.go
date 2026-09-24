package recall

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func TestRecallHypothesisMetricsDistinguishOutcomesAndCountReturned(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	search := func() *recallSearchStub {
		return &recallSearchStub{
			contract: &searchcontract.ActiveSearchContract{EmbeddingContractID: uuid.NewString(), EmbeddingDimensions: 3},
			result: &recallcontract.RecallEvidenceResult{
				SearchState: string(domain.SearchProjectionCurrent),
				Results:     []recallcontract.RecallEvidenceHit{{EvidenceID: evidenceID, Rank: 1, Context: "PostgreSQL memory"}},
			},
		}
	}
	metrics := observability.NewPrometheusMetrics()
	hypotheses := &recallHypothesisStub{records: []dreamcontract.HypothesisRecord{{
		HypothesisID: uuid.NewString(), Status: string(domain.DreamStatusProposed),
		Statement: "Dense-Mem may benefit from explicit search freshness.", GeneratorKind: "server",
		GeneratorVersion: "dream-v2.candidate-safe", CreatedAt: time.Now().UTC(),
	}}}
	result, err := NewRecallService(RecallDependencies{Search: search(), Hypotheses: hypotheses, Metrics: metrics}).Recall(
		authenticatedRememberContext(teamID, profileID, keyID),
		RecallRequest{Query: "PostgreSQL memory", IncludeHypotheses: true},
	)
	require.NoError(t, err)
	require.Len(t, result.RelatedHypotheses, 1)

	for _, test := range []struct {
		name       string
		requested  bool
		hypotheses *recallHypothesisStub
	}{
		{name: "empty", requested: true, hypotheses: &recallHypothesisStub{}},
		{name: "unavailable", requested: true, hypotheses: &recallHypothesisStub{err: errors.New("ledger unavailable")}},
		{name: "missing repository", requested: true},
		{name: "not requested", hypotheses: &recallHypothesisStub{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var repository RecallHypothesisRepository
			if test.hypotheses != nil {
				repository = test.hypotheses
			}
			_, err := NewRecallService(RecallDependencies{Search: search(), Hypotheses: repository, Metrics: metrics}).Recall(
				authenticatedRememberContext(teamID, profileID, keyID),
				RecallRequest{Query: "PostgreSQL memory", IncludeHypotheses: test.requested},
			)
			require.NoError(t, err)
		})
	}

	metricText := recallMetricsText(t, metrics)
	require.Contains(t, metricText, `densemem_recall_hypothesis_expansions_total{outcome="returned"} 1`)
	require.Contains(t, metricText, `densemem_recall_hypotheses_returned_total 1`)
	require.Contains(t, metricText, `densemem_recall_hypothesis_expansions_total{outcome="empty"} 1`)
	require.Contains(t, metricText, `densemem_recall_hypothesis_expansions_total{outcome="unavailable"} 2`)
	require.Contains(t, metricText, `densemem_recall_hypothesis_expansions_total{outcome="not_requested"} 1`)
}
