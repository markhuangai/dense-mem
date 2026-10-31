package evalharness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOntologyMatchingBaseline(t *testing.T) {
	cohort := OntologyMatchingCohort()
	encoded, err := json.Marshal(cohort)
	require.NoError(t, err)
	generator, err := os.ReadFile("organization_matching_cohort.go")
	require.NoError(t, err)
	data, err := os.ReadFile("../../tests/eval/source_locks/ontology_matching_v1.json")
	require.NoError(t, err)
	var lock struct {
		CohortSHA256    string `json:"cohort_sha256"`
		GeneratorSHA256 string `json:"generator_sha256"`
		CaseCount       int    `json:"case_count"`
		SourceCount     int    `json:"source_count"`
	}
	require.NoError(t, json.Unmarshal(data, &lock))
	require.Equal(t, lock.CohortSHA256, fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)))
	require.Equal(t, lock.GeneratorSHA256, fmt.Sprintf("sha256:%x", sha256.Sum256(generator)))
	require.Len(t, cohort, lock.CaseCount)
	sources := 0
	var unorganized OrganizationScore
	for _, item := range cohort {
		sources += len(item.Case.Sources)
		score, err := ScoreOrganization(item.Case, UnorganizedItems(item.Case))
		require.NoError(t, err)
		require.Equal(t, 1.0, score.SourcePreservation)
		require.Equal(t, 1.0, score.DistinctFactCoverage)
		require.Zero(t, score.FalseConsolidations)
		unorganized.RepeatedSlots += score.RepeatedSlots
		unorganized.FalseConsolidations += score.FalseConsolidations
		unorganized.DistinctFacts += score.DistinctFacts
		unorganized.RetainedFacts += score.RetainedFacts
		unorganized.OriginalSources += score.OriginalSources
		unorganized.RetainedSources += score.RetainedSources
		all := []string{item.Case.Sources[0].ID, item.Case.Sources[1].ID}
		grouped, err := ScoreOrganization(item.Case, []OrganizationItem{{ID: "group", SourceIDs: all}})
		require.NoError(t, err)
		if item.Case.Sources[0].EquivalenceKey != item.Case.Sources[1].EquivalenceKey {
			require.Equal(t, 1, grouped.FalseConsolidations, item.Case.ID)
		} else {
			require.Zero(t, grouped.FalseConsolidations)
			require.Zero(t, grouped.RepeatedSlots)
		}
	}
	require.Equal(t, lock.SourceCount, sources)
	unorganized.SourcePreservation = float64(unorganized.RetainedSources) / float64(unorganized.OriginalSources)
	unorganized.DistinctFactCoverage = float64(unorganized.RetainedFacts) / float64(unorganized.DistinctFacts)
	data, err = os.ReadFile("../../tests/eval/baselines/ontology_matching_v1.json")
	require.NoError(t, err)
	var baseline struct {
		CohortSHA256 string            `json:"cohort_sha256"`
		CaseCount    int               `json:"case_count"`
		Score        OrganizationScore `json:"score"`
	}
	require.NoError(t, json.Unmarshal(data, &baseline))
	require.Equal(t, lock.CohortSHA256, baseline.CohortSHA256)
	require.Equal(t, lock.CaseCount, baseline.CaseCount)
	require.Equal(t, unorganized, baseline.Score)
}
