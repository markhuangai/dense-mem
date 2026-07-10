package evalharness

import "testing"

func TestScoreTracesScoresFragmentContextRefsAsEvidenceFallback(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"evidence"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which source backs alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredRefs:         []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
			BadEvidenceRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-source-bad"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:      "case-1",
		Query:       "which source backs alpha?",
		RankedRefs:  []Ref{{Type: "fragment", ID: "fragment-good"}},
		ContextRefs: []Ref{{Type: "fragment", ID: "fragment-good"}, {Type: "fragment", ID: "fragment-bad"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-source-good": {Type: "fragment", ID: "fragment-good", SourceDocID: "doc-source-good"},
		"doc-source-bad":  {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-source-bad"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if !score.EvidenceScored || score.EvidenceRecallAtK != 1 || score.EvidenceBadAtK != 1 || score.EvidenceFirstRequiredRank != 1 || score.EvidenceFirstBadRank != 2 {
		t.Fatalf("fallback evidence score = %+v", score)
	}
	if len(score.EvidenceMissingRequired) != 0 || len(score.EvidenceBadRefsAtK) != 1 {
		t.Fatalf("fallback evidence refs = %+v", score)
	}
	if summary.AverageEvidenceRecallAtK != 1 || summary.AverageEvidenceBadAtK != 1 || summary.Slices["evidence"].AverageEvidenceRecallAtK != 1 {
		t.Fatalf("fallback evidence summary = %+v", summary)
	}
}
