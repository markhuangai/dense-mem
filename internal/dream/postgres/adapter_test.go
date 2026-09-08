package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHypothesisEvaluationQueryBindsFilters(t *testing.T) {
	query, args, err := HypothesisEvaluationQuery(EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "hypothesis",
		Status: "active",
		Limit:  1,
	}, 1, 0, "00000000-0000-0000-0000-000000000202")
	require.NoError(t, err)
	require.Contains(t, query, "canonical_hypothesis_id IS NULL")
	require.Contains(t, query, "AND status = ?")
	require.Equal(t, []any{
		"00000000-0000-0000-0000-000000000101",
		"00000000-0000-0000-0000-000000000202",
		"active",
		1,
		0,
	}, args)
}

func TestDreamStoreExposesSeparateDailyAndScheduledPorts(t *testing.T) {
	var store *Store
	var _ DreamRepository = store
	var _ ScheduledDreamRepository = store
	var _ EvidenceDiscoveryRepository = store
}
