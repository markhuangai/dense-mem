//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/stretchr/testify/require"
)

func TestRecallOntologyFrozenCohort(t *testing.T) {
	cohort := evalharness.OntologyOrganizationCohort()
	encoded, err := json.Marshal(cohort)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	cohortHash := "sha256:" + hex.EncodeToString(digest[:])
	var baseline struct {
		CohortSHA256 string `json:"cohort_sha256"`
		CaseCount    int    `json:"case_count"`
		SourceCount  int    `json:"source_count"`
	}
	data, err := os.ReadFile("../../../tests/eval/baselines/ontology_organization_v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &baseline))
	require.Equal(t, baseline.CohortSHA256, cohortHash)
	require.Len(t, cohort, baseline.CaseCount)
	reports := map[string]any{}
	totalSources, beforeRepetitions, afterRepetitions := 0, 0, 0
	for _, testCase := range cohort {
		t.Run(testCase.ID, func(t *testing.T) {
			f := newRecallOntologyFixture(t)
			handles := []ontology.SourceHandle{}
			meanings, goldIDs := map[string]string{}, map[string]string{}
			for i, source := range testCase.Sources {
				owner := i % 2
				if testCase.Override == "group_together" {
					owner = 0
				}
				handle := f.evidence(t, owner, source.Text, map[string]any{"actor": source.Actor, "polarity": source.Polarity, "time": source.Time, "qualification": source.Qualification})
				handles = append(handles, handle)
				meanings[source.Text], goldIDs[handle.ID] = source.EquivalenceKey, source.ID
			}
			if testCase.Override != "" {
				f.override(t, ontology.OverrideAction(testCase.Override), handles)
			}
			provider, calls := f.organizer(t, meanings)
			_, err := provider.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
			require.NoError(t, err)
			priorCalls := calls.Load()
			input := RecallEvidenceInput{TeamID: f.team, Query: "PostgreSQL", Limit: 50, SpaceID: f.space, QueryEmbedding: []float32{1, 0, 0}}
			before, err := f.search.RecallEvidence(f.actor(2, "member"), input)
			require.NoError(t, err)
			input.OrganizationEnabled = true
			after, err := f.search.RecallEvidence(f.actor(2, "member"), input)
			require.NoError(t, err)
			scoreResult := func(result *recallcontract.RecallEvidenceResult) evalharness.OrganizationScore {
				items := []evalharness.OrganizationItem{}
				for _, hit := range result.Results {
					ids := []string{}
					for _, id := range append([]string{hit.EvidenceID}, hit.EquivalentEvidenceIDs...) {
						require.NotEmpty(t, goldIDs[id], "unexpected source is a Bad@K failure")
						ids = append(ids, goldIDs[id])
					}
					items = append(items, evalharness.OrganizationItem{ID: hit.EvidenceID, SourceIDs: ids})
				}
				score, err := evalharness.ScoreOrganization(testCase, items)
				require.NoError(t, err)
				require.Equal(t, 1.0, score.DistinctFactCoverage)
				require.Equal(t, 1.0, score.SourcePreservation)
				require.Zero(t, score.FalseConsolidations)
				return score
			}
			beforeScore, afterScore := scoreResult(before), scoreResult(after)
			if testCase.ID == "exact_duplicates" || testCase.ID == "paraphrases" || testCase.ID == "manager_grouping" {
				require.Less(t, afterScore.RepeatedSlots, beforeScore.RepeatedSlots)
			}
			if testCase.ID == "manager_separation" {
				require.Len(t, after.Results, len(handles))
			}
			require.Equal(t, priorCalls, calls.Load(), "Recall added a provider call")
			totalSources += len(handles)
			beforeRepetitions += beforeScore.RepeatedSlots
			afterRepetitions += afterScore.RepeatedSlots
			reports[testCase.ID] = map[string]any{"before": beforeScore, "after": afterScore, "baseline_bad_at_k": 0, "candidate_bad_at_k": 0, "recall_provider_calls": calls.Load() - priorCalls}
		})
	}
	require.Equal(t, baseline.SourceCount, totalSources)
	require.Less(t, afterRepetitions, beforeRepetitions)
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		report := map[string]any{"schema_version": "dense-mem.ontology.recall_comparison.v1", "issue": 244, "cohort_sha256": cohortHash, "case_count": len(cohort), "source_count": totalSources, "baseline_repeated_slots": beforeRepetitions, "candidate_repeated_slots": afterRepetitions, "false_consolidations": 0, "source_preservation": 1, "distinct_fact_coverage": 1, "baseline_bad_at_k": 0, "candidate_bad_at_k": 0, "recall_provider_calls": 0, "verified": !t.Failed(), "case_scores": reports}
		output, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(directory, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "ontology-recall-comparison.json"), append(output, '\n'), 0600))
	}
}
