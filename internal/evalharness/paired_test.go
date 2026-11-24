package evalharness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuestionCheckpointRequiresBothPoliciesAndExactPreparedIdentity(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{OutputDir: dir, WorkflowRunID: "101", Dataset: "hotpotqa", PairArm: "candidate", SeedImage: "seed-a", PairShard: 0, PairShards: 4}
	q := "public/question"
	rows := []CaseScore{{QuestionID: q, Condition: "on", Policy: "single", Completed: true}, {QuestionID: q, Condition: "on", Policy: "multi_turn", Completed: true}}
	require.NoError(t, checkpointQuestion(m, q, rows))
	t.Setenv("EVAL_RESUME_RUN", m.WorkflowRunID)
	t.Setenv("EVAL_RESUME_QUESTIONS", filepath.Join(dir, "questions"))
	got, err := restoredQuestion(m, q)
	require.NoError(t, err)
	require.Equal(t, rows, got)
	changed := m
	changed.SeedImage = "seed-b"
	_, err = restoredQuestion(changed, q)
	require.ErrorContains(t, err, "frozen evaluation identity")
	rows[0].Completed = false
	require.NoError(t, checkpointQuestion(m, "unfinished", rows))
	_, err = os.Stat(checkpointFile(filepath.Join(dir, "questions"), "unfinished"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestPreparedReportCountsTwoPoliciesOnce(t *testing.T) {
	m := Manifest{PairArm: "candidate"}
	report := NewReport(m, 1, 1)
	report.ImportedRows, report.CaptureComplete = 1, true
	report.Scores = []CaseScore{{QuestionID: "q", Condition: "on", Policy: "single", Completed: true}, {QuestionID: "q", Condition: "on", Policy: "multi_turn", Completed: true}}
	require.NoError(t, FinalizeReport(&report))
	require.True(t, report.Complete)
	require.Equal(t, 2, report.ExpectedAnswers)
	require.Len(t, report.Matrix, 2)
	require.Empty(t, report.OntologyDelta)
}

func TestTopicPairScoreIncludesFalsePositivesMissingSourcesAndMultipleLabels(t *testing.T) {
	sources := []judgedTopicSource{
		{ID: "a", Labels: []string{"film", "detective"}},
		{ID: "b", Labels: []string{"detective"}},
		{ID: "c", Labels: []string{"river"}},
		{ID: "d", Labels: []string{"film"}},
	}
	result := scoreTopicPairs(sources, map[string][]string{"a": {"one", "one", "two"}, "b": {"one"}, "c": {"two"}})
	require.Equal(t, 6, result.Pairs)
	require.Equal(t, 1, result.TruePositive)
	require.Equal(t, 1, result.FalsePositive)
	require.Equal(t, 1, result.FalseNegative)
	require.Equal(t, 3, result.TrueNegative)
	require.Equal(t, 1, result.UnassignedSources)
	require.Equal(t, 0.5, *result.Precision)
	require.Equal(t, 0.5, *result.Recall)
	empty := scoreTopicPairs(sources, map[string][]string{})
	require.Nil(t, empty.Precision)
	require.Zero(t, *empty.Recall)
}

func TestApplicationPoolDeltaRequiresSameProcessAndCompleteCounters(t *testing.T) {
	before := &applicationPoolCounters{ProcessStartedAt: "start", WaitCount: new(int64(2)), WaitDurationNS: new(int64(90))}
	after := &applicationPoolCounters{ProcessStartedAt: "start", WaitCount: new(int64(5)), WaitDurationNS: new(int64(150))}
	result, err := compareApplicationPool(before, after)
	require.NoError(t, err)
	require.Equal(t, int64(3), *result.WaitCount)
	require.Equal(t, int64(60), *result.WaitDurationNS)
	unavailable, err := compareApplicationPool(nil, nil)
	require.NoError(t, err)
	require.False(t, unavailable.Available)
	require.Nil(t, unavailable.WaitCount)
	require.Nil(t, unavailable.WaitDurationNS)
	after.ProcessStartedAt = "restarted"
	_, err = compareApplicationPool(before, after)
	require.Error(t, err)
	_, err = compareApplicationPool(before, nil)
	require.Error(t, err)
}

func TestDeliveredAliasTextRequiresAnExactCanonicalCitation(t *testing.T) {
	q, observation, corpus, mapping, canonical := scoringFixture()
	initial := MemoryCall{Tool: "recall_memory", Result: map[string]any{"results": []any{map[string]any{"evidence_id": testEvidenceA1, "context": "Alpha. Extra occurrence words."}}}}
	observation.Initial = initial
	for i := range observation.Hosts {
		observation.Hosts[i].Calls = []MemoryCall{initial}
		observation.Hosts[i].Citations = []Citation{{testEvidenceA1, "Alpha."}, {testEvidenceA1, "Extra occurrence words."}}
	}
	scores, err := ScoreObservation(q, observation, corpus, mapping, canonical)
	require.NoError(t, err)
	for _, score := range scores {
		require.Equal(t, 2, score.Citations)
		require.Equal(t, 1, score.ValidCitations)
	}
}
