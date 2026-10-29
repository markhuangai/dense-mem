package evalharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func accuracyReportFixture(t *testing.T) (PairConfig, RecallAccuracyExperiment, string) {
	t.Helper()
	c := pairedFixture(t)
	for _, side := range []string{"baseline", "candidate"} {
		for _, stage := range []string{"import", "capture", "score"} {
			writePairCompletedStage(t, c, side, stage)
		}
	}
	config := filepath.Join(t.TempDir(), "pair.json")
	require.NoError(t, writePairJSON(config, c))
	manifest := RecallAccuracyExperiment{SchemaVersion: RecallAccuracyReportSchema, PairConfigs: []string{config}, OutDir: filepath.Join(t.TempDir(), "report")}
	path := filepath.Join(t.TempDir(), "experiment.json")
	require.NoError(t, writePairJSON(path, manifest))
	return c, manifest, path
}

func TestRecallAccuracyReportAggregatesNativeScoresWithoutChangingReceipts(t *testing.T) {
	c, manifest, path := accuracyReportFixture(t)
	before, err := pairArtifactHashes(c.OutDir)
	require.NoError(t, err)
	report, err := WriteRecallAccuracyReport(path)
	require.NoError(t, err)
	require.True(t, report.Passed)
	require.Len(t, report.Repetitions, 1)
	run := report.Repetitions[0]
	require.Len(t, run.Cases, 2)
	require.NotEmpty(t, run.Pair.Sides["candidate"].Scores.Slices)
	require.NotEmpty(t, run.AxisRankOne["candidate"])
	require.Equal(t, 2, run.LatencyMS["candidate"].Missing)
	require.Zero(t, report.Variability["recall_delta"].Mean)
	for _, row := range run.Cases {
		require.Equal(t, row.Baseline, row.Candidate)
		require.False(t, row.Regression)
	}
	after, err := pairArtifactHashes(c.OutDir)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.FileExists(t, filepath.Join(manifest.OutDir, "recall_accuracy_report.md"))
	_, err = WriteRecallAccuracyReport(path)
	require.ErrorIs(t, err, os.ErrExist)
}

func TestRecallAccuracyReportRejectsUnprovenOrDuplicateRuns(t *testing.T) {
	for _, scenario := range []string{"duplicate", "missing_capture", "drift", "overlap", "unknown_field"} {
		t.Run(scenario, func(t *testing.T) {
			c, manifest, path := accuracyReportFixture(t)
			switch scenario {
			case "duplicate":
				manifest.PairConfigs = append(manifest.PairConfigs, manifest.PairConfigs[0])
			case "missing_capture":
				dir, err := latestPairStage(c, "candidate", "capture")
				require.NoError(t, err)
				require.NoError(t, os.Remove(filepath.Join(dir, "native", "pair_capture.json")))
			case "drift":
				dir, err := latestPairStage(c, "candidate", "score")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "native", "summary.json"), []byte("{}"), 0o600))
			case "overlap":
				manifest.OutDir = filepath.Join(c.OutDir, "report")
			}
			require.NoError(t, writePairJSON(path, manifest))
			if scenario == "unknown_field" {
				require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":"dense-mem.eval.recall_accuracy_report.v1","unexpected":true}`), 0o600))
			}
			_, err := WriteRecallAccuracyReport(path)
			require.Error(t, err)
			require.NoDirExists(t, manifest.OutDir)
		})
	}
}

func TestRecallAccuracyReportRejectsMismatchedAggregateAndCaseAccounting(t *testing.T) {
	c, _, _ := accuracyReportFixture(t)
	dir, err := latestPairStage(c, "candidate", "score")
	require.NoError(t, err)
	dir = filepath.Join(dir, "native")
	seed, _, cases, _, _, suite, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	require.NoError(t, err)
	var summary Summary
	require.NoError(t, readJSONFile(filepath.Join(dir, "summary.json"), &summary))
	changed := summary
	changed.AverageRecallAtK += .1
	_, err = readAccuracyScores(c, dir, changed, seed, cases, suite)
	require.ErrorContains(t, err, "aggregate summary differs")
	var scores []RetrievalScore
	require.NoError(t, readJSONL(filepath.Join(dir, "retrieval_scores.jsonl"), &scores))
	traces, err := LoadRecallTraces(filepath.Join(dir, "recall_traces.jsonl"))
	require.NoError(t, err)
	for _, scenario := range []string{"duplicate_score", "missing_score", "unknown_score", "missing_trace", "unknown_trace"} {
		t.Run(scenario, func(t *testing.T) {
			changedScores := append([]RetrievalScore(nil), scores...)
			changedTraces := append([]RecallTrace(nil), traces...)
			switch scenario {
			case "duplicate_score":
				changedScores = append(changedScores, scores[0])
			case "missing_score":
				changedScores = changedScores[:len(changedScores)-1]
			case "unknown_score":
				changedScores[0].CaseID = "unknown-case"
			case "missing_trace":
				changedTraces = changedTraces[:len(changedTraces)-1]
			case "unknown_trace":
				changedTraces[0].CaseID = "unknown-case"
			}
			require.NoError(t, writeJSONL(filepath.Join(dir, "retrieval_scores.jsonl"), changedScores))
			require.NoError(t, writeJSONL(filepath.Join(dir, "recall_traces.jsonl"), changedTraces))
			_, err = readAccuracyScores(c, dir, summary, seed, cases, suite)
			require.ErrorContains(t, err, "missing, duplicate or unknown")
		})
	}
}

func TestRecallAccuracyReportLatencyDistribution(t *testing.T) {
	values := []float64{40, 10, 30, 20}
	stats := accuracyDistribution(values, 2)
	require.Equal(t, []float64{40, 10, 30, 20}, values)
	require.Equal(t, 4, stats.Count)
	require.Equal(t, 2, stats.Missing)
	require.Equal(t, 25.0, stats.Mean)
	require.Equal(t, 10.0, stats.Min)
	require.Equal(t, 40.0, stats.Max)
	require.Equal(t, 20.0, stats.P50)
	require.Equal(t, 40.0, stats.P95)
	require.Equal(t, 40.0, stats.P99)
	require.InDelta(t, 11.1803398875, stats.StdDev, 1e-9)
	require.Equal(t, RecallAccuracyDistribution{Missing: 3}, accuracyDistribution(nil, 3))
}
