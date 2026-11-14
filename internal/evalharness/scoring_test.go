package evalharness

import (
	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testEvidenceA1    = "11111111-1111-4111-8111-111111111111"
	testEvidenceA2    = "22222222-2222-4222-8222-222222222222"
	testEvidenceB     = "33333333-3333-4333-8333-333333333333"
	testEvidenceOther = "44444444-4444-4444-8444-444444444444"
)

func scoringFixture() (Question, Observation, map[string]CorpusItem, KnowledgeMapping, map[string]CanonicalEvidence) {
	q := Question{ID: "question", Query: "Which storage?", Answers: []string{"The PostgreSQL database", "Postgres"}, GoldSources: []string{"source-a", "source-b"}}
	corpus := map[string]CorpusItem{"source-a": {SourceDocID: "source-a", Content: "Alpha. Beta."}, "source-b": {SourceDocID: "source-b", Content: "Gamma."}, "unjudged": {SourceDocID: "unjudged", Content: "Unjudged."}}
	mapping := newKnowledgeMapping()
	mapping.Partitions = map[string][]ImportedPartition{"source-a": {{EvidenceID: testEvidenceA1, SpanStart: 0, SpanEnd: 7}, {EvidenceID: testEvidenceA2, SpanStart: 7, SpanEnd: 12}}, "source-b": {{EvidenceID: testEvidenceB, SpanStart: 0, SpanEnd: 6}}, "unjudged": {{EvidenceID: testEvidenceOther, SpanStart: 0, SpanEnd: 9}}}
	for source, parts := range mapping.Partitions {
		for i := range parts {
			parts[i].PartitionIndex = i
			parts[i].SourceItemID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(source)).String()
			parts[i].OccurrenceID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(source+parts[i].EvidenceID)).String()
			parts[i].ContentHash = knowledge.EvidenceContentHash(string([]rune(corpus[source].Content)[parts[i].SpanStart:parts[i].SpanEnd]))
		}
		mapping.Partitions[source] = parts
	}
	canonical := map[string]CanonicalEvidence{testEvidenceA1: {SourceID: "source-a", Content: "Alpha. "}, testEvidenceA2: {SourceID: "source-a", Content: "Beta."}, testEvidenceB: {SourceID: "source-b", Content: "Gamma."}, testEvidenceOther: {SourceID: "unjudged", Content: "Unjudged."}}
	initial := MemoryCall{Tool: "recall_memory", Result: map[string]any{"results": []any{map[string]any{"evidence_id": testEvidenceA1, "context": "Alpha. ", "equivalent_evidence_ids": []any{testEvidenceB}}, map[string]any{"evidence_id": testEvidenceA2, "context": "Beta."}, map[string]any{"evidence_id": testEvidenceB, "context": "truncated"}}}}
	hosts := []HostResult{}
	for _, policy := range []string{"single", "multi_turn"} {
		hosts = append(hosts, HostResult{Policy: policy, Answer: "Postgres", Citations: []Citation{{EvidenceID: testEvidenceA1, Quote: "Alpha."}, {EvidenceID: testEvidenceB, Quote: "Gamma."}}, Calls: []MemoryCall{initial}})
	}
	return q, Observation{QuestionID: q.ID, Condition: "off", Initial: initial, Hosts: hosts}, corpus, mapping, canonical
}

func TestScoringSeparatesRankedSourcesDeliveredTextAnswersAndCitations(t *testing.T) {
	q, observation, corpus, mapping, canonical := scoringFixture()
	scores, err := ScoreObservation(q, observation, corpus, mapping, canonical)
	require.NoError(t, err)
	require.Len(t, scores, 2)
	for _, score := range scores {
		require.Equal(t, 0.5, score.Recall1)
		require.Equal(t, 1.0, score.Recall5)
		require.Equal(t, 0.5, score.InitialTextCoverage)
		require.Equal(t, 1.0, score.EM)
		require.Equal(t, 1, score.ValidCitations)
		require.Equal(t, 2, score.Citations)
	}
	observation.Initial.Result["results"] = []any{map[string]any{"evidence_id": testEvidenceOther, "context": "Unjudged.", "equivalent_evidence_ids": []any{testEvidenceA1, testEvidenceA2, testEvidenceB}}}
	for i := range observation.Hosts {
		observation.Hosts[i].Calls[0] = observation.Initial
	}
	scores, err = ScoreObservation(q, observation, corpus, mapping, canonical)
	require.NoError(t, err)
	require.Zero(t, scores[0].Recall10)
	require.Zero(t, scores[0].FinalTextCoverage)
}

func TestAnswerNormalizationAndFailureDenominators(t *testing.T) {
	em, f1 := AnswerScore("The, quick fox!", []string{"quick fox", "wrong"})
	require.Equal(t, 1.0, em)
	require.Equal(t, 1.0, f1)
	em, f1 = AnswerScore("red red blue", []string{"red blue blue"})
	require.Zero(t, em)
	require.InDelta(t, 2.0/3, f1, 1e-10)
	q, observation, corpus, mapping, canonical := scoringFixture()
	observation.Hosts[0].Error = "provider failed"
	scores, err := ScoreObservation(q, observation, corpus, mapping, canonical)
	require.NoError(t, err)
	require.Zero(t, scores[0].EM)
	report := NewReport(Manifest{}, 3, 1000)
	report.Scores = scores
	report.CaptureComplete = true
	report.ImportedRows = 3
	require.NoError(t, FinalizeReport(&report))
	require.Equal(t, 4000, report.ExpectedAnswers)
	require.Equal(t, 1, report.CompletedAnswers)
	require.False(t, report.Complete)
	require.Equal(t, 999, report.Matrix[0].Missing)
	require.Equal(t, 1, report.Matrix[0].Failed)
	report.Scores = append(report.Scores, scores[0])
	require.ErrorContains(t, FinalizeReport(&report), "duplicate")
}
