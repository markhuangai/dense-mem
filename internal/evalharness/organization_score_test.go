package evalharness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOntologyOrganizationBaseline(t *testing.T) {
	cohort := OntologyOrganizationCohort()
	encoded, err := json.Marshal(cohort)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	generator, err := os.ReadFile("organization_cohort.go")
	require.NoError(t, err)
	generatorDigest := sha256.Sum256(generator)
	lockBytes, err := os.ReadFile("../../tests/eval/source_locks/ontology_organization_v1.json")
	require.NoError(t, err)
	var lock struct {
		GeneratorSHA256 string `json:"generator_sha256"`
		CaseCount       int    `json:"case_count"`
		SourceCount     int    `json:"source_count"`
	}
	require.NoError(t, json.Unmarshal(lockBytes, &lock))
	require.Equal(t, lock.GeneratorSHA256, "sha256:"+hex.EncodeToString(generatorDigest[:]))
	require.Len(t, cohort, lock.CaseCount)
	scores := map[string]OrganizationScore{}
	var sources, repetitions, facts int
	for _, testCase := range cohort {
		score, err := ScoreOrganization(testCase, UnorganizedItems(testCase))
		require.NoError(t, err)
		require.Equal(t, 1.0, score.DistinctFactCoverage)
		require.Equal(t, 1.0, score.SourcePreservation)
		require.Zero(t, score.FalseConsolidations)
		sources += score.OriginalSources
		repetitions += score.RepeatedSlots
		facts += score.DistinctFacts
		scores[testCase.ID] = score
	}
	require.Equal(t, lock.SourceCount, sources)
	require.Equal(t, 4, repetitions)
	require.Equal(t, 14, facts)
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
		report := map[string]any{"schema_version": "dense-mem.ontology.baseline.v1", "scope": "synthetic unorganized baseline; no active retrieval improvement claimed", "cohort_sha256": "sha256:" + hex.EncodeToString(digest[:]), "case_count": len(cohort), "source_count": sources, "repeated_slots": repetitions, "distinct_facts": facts, "scores": scores}
		output, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "baseline.json"), output, 0600))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "cohort.json"), encoded, 0600))
	}
}

func TestOrganizationScoringDetectsLostFactsAndFalseConsolidation(t *testing.T) {
	for _, testCase := range OntologyOrganizationCohort() {
		all := []string{}
		for _, source := range testCase.Sources {
			all = append(all, source.ID)
		}
		score, err := ScoreOrganization(testCase, []OrganizationItem{{ID: "one", SourceIDs: all}})
		require.NoError(t, err)
		if testCase.ID == "exact_duplicates" || testCase.ID == "paraphrases" || testCase.ID == "manager_grouping" {
			require.Zero(t, score.FalseConsolidations)
		} else {
			require.Equal(t, 1, score.FalseConsolidations, testCase.ID)
		}
		empty, err := ScoreOrganization(testCase, nil)
		require.NoError(t, err)
		require.Zero(t, empty.DistinctFactCoverage)
		require.Zero(t, empty.SourcePreservation)
	}
	partial := OntologyOrganizationCohort()[2]
	score, err := ScoreOrganization(partial, []OrganizationItem{{ID: "only-postgres", SourceIDs: []string{partial.Sources[1].ID}}})
	require.NoError(t, err)
	require.Equal(t, 0.5, score.DistinctFactCoverage)
	require.Equal(t, 0.5, score.SourcePreservation)
	for _, items := range [][]OrganizationItem{{{ID: "unknown", SourceIDs: []string{"not-admitted"}}}, {{ID: "empty"}}, {{ID: "same", SourceIDs: []string{partial.Sources[0].ID}}, {ID: "same", SourceIDs: []string{partial.Sources[1].ID}}}} {
		_, err := ScoreOrganization(partial, items)
		require.Error(t, err)
	}
	_, err = ScoreOrganization(OrganizationCase{}, nil)
	require.Error(t, err)
	invalid := partial
	invalid.Sources = append(invalid.Sources, invalid.Sources[0])
	_, err = ScoreOrganization(invalid, nil)
	require.Error(t, err)
}
