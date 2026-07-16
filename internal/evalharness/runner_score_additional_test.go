package evalharness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunLiveSuiteAndCompareErrorBranches(t *testing.T) {
	_, err := runLiveSuite(context.Background(), &HTTPClient{}, []SuiteCase{{CaseID: "case-1"}}, map[string]Case{
		"case-1": {CaseID: "case-1", Query: "query"},
	})
	if err == nil {
		t.Fatal("runLiveSuite error = nil")
	}

	dir := t.TempDir()
	baselineDir := filepath.Join(dir, "baseline")
	candidateDir := filepath.Join(dir, "candidate")
	if err := writeJSONFile(filepath.Join(baselineDir, "summary.json"), Summary{SeedHash: "sha256:a"}); err != nil {
		t.Fatalf("write baseline summary: %v", err)
	}
	if err := writeJSONFile(filepath.Join(candidateDir, "summary.json"), Summary{SeedHash: "sha256:b"}); err != nil {
		t.Fatalf("write candidate summary: %v", err)
	}
	if _, err := CompareRunDirs(baselineDir, candidateDir, ""); err == nil {
		t.Fatal("CompareRunDirs seed mismatch error = nil")
	}
}

func TestScoreTracesUsesMappingAndPenalizesUnmappedRefs(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"direct"}}, {CaseID: "case-2", Slices: []string{"direct"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "alpha?", Limit: 2},
		"case-2": {CaseID: "case-2", Query: "missing?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-alpha", Grade: 2}},
			BadRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-beta"}},
		},
		"case-2": {
			CaseID:       "case-2",
			RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-unmapped", Grade: 1}},
		},
	}
	traces := []RecallTrace{
		{
			CaseID:     "case-1",
			Query:      "alpha?",
			RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha"}, {Type: "fragment", ID: "frag-beta"}},
			LatencyMS:  10,
		},
		{
			CaseID:     "case-2",
			Query:      "missing?",
			RankedRefs: []Ref{{Type: "fragment", ID: "frag-alpha"}},
			LatencyMS:  11,
		},
	}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-alpha": {Type: "fragment", ID: "frag-alpha", SourceDocID: "doc-alpha"},
		"doc-beta":  {Type: "fragment", ID: "frag-beta", SourceDocID: "doc-beta"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("scores = %d; want 2", len(scores))
	}
	if scores[0].RecallAtK != 1 || scores[0].BadAtK != 1 || len(scores[0].MissingRequired) != 0 {
		t.Fatalf("score[0] = %+v", scores[0])
	}
	if scores[0].FirstRequiredRank != 1 || scores[0].FirstBadRank != 2 {
		t.Fatalf("score[0] ranks = %+v", scores[0])
	}
	if scores[1].RelevantTotal != 1 || scores[1].RecallAtK != 0 || len(scores[1].UnmappedSourceRefs) != 1 {
		t.Fatalf("score[1] = %+v", scores[1])
	}
	if summary.AverageRecallAtK != 0.5 || summary.AverageBadAtK != 0.5 || summary.RequiredRank1Rate != 0.5 || summary.BadRank1Rate != 0 || summary.UnmappedSourceRefs != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Slices["direct"].CaseCount != 2 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func TestScoreTracesScoresContextRefsSeparately(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"context"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-alpha"}},
			BadRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-beta"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:      "case-1",
		Query:       "alpha?",
		RankedRefs:  []Ref{{Type: "fragment", ID: "frag-alpha"}},
		ContextRefs: []Ref{{Type: "fragment", ID: "frag-beta"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-alpha": {Type: "fragment", ID: "frag-alpha", SourceDocID: "doc-alpha"},
		"doc-beta":  {Type: "fragment", ID: "frag-beta", SourceDocID: "doc-beta"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.BadAtK != 0 || score.FirstRequiredRank != 1 {
		t.Fatalf("ranked score = %+v", score)
	}
	if !score.ContextScored || score.ContextRecallAtK != 0 || score.ContextBadAtK != 1 || score.ContextFirstBadRank != 1 {
		t.Fatalf("context score = %+v", score)
	}
	if len(score.ContextMissingRequired) != 1 || len(score.ContextBadRefsAtK) != 1 {
		t.Fatalf("context miss/bad refs = %+v", score)
	}
	if summary.AverageRecallAtK != 1 || summary.AverageBadAtK != 0 {
		t.Fatalf("ranked summary = %+v", summary)
	}
	if summary.ContextScoredCaseCount != 1 || summary.AverageContextRecallAtK != 0 || summary.AverageContextBadAtK != 1 || summary.ContextBadRank1Rate != 1 {
		t.Fatalf("context summary = %+v", summary)
	}
	if summary.Slices["context"].ContextScoredCaseCount != 1 || summary.Slices["context"].AverageContextBadAtK != 1 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func TestScoreTracesScoresFactQRelsThroughSameSourceFragments(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"project_graph"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which ADR applies?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:       "case-1",
			RequiredRefs: []Ref{{Type: "fact", SourceDocID: "doc-required:fact:1", Grade: 3}},
			BadRefs:      []Ref{{Type: "fact", SourceDocID: "doc-stale:fact:1", Grade: -1}},
		},
	}
	traces := []RecallTrace{{
		CaseID:      "case-1",
		Query:       "which ADR applies?",
		RankedRefs:  []Ref{{Type: "fragment", ID: "fragment-required"}, {Type: "fragment", ID: "fragment-stale"}},
		ContextRefs: []Ref{{Type: "fragment", ID: "fragment-required"}, {Type: "fragment", ID: "fragment-stale"}},
	}}
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-required", SourceDocID: "doc-required"}, true)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-required", SourceDocID: "doc-required"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-required", SourceDocID: "doc-required:fact:1"}, false)
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-stale", SourceDocID: "doc-stale"}, true)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-stale", SourceDocID: "doc-stale"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-stale", SourceDocID: "doc-stale:fact:1"}, false)

	scores, summary, err := ScoreTraces("run-1", "candidate", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.BadAtK != 1 || score.FirstRequiredRank != 1 || score.FirstBadRank != 2 {
		t.Fatalf("fragment-backed fact score = %+v", score)
	}
	if len(score.MissingRequired) != 0 || len(score.BadRefsAtK) != 1 {
		t.Fatalf("fragment-backed fact refs = %+v", score)
	}
	if !score.ContextScored || score.ContextRecallAtK != 1 || score.ContextBadAtK != 1 {
		t.Fatalf("fragment-backed context score = %+v", score)
	}
	if summary.AverageRecallAtK != 1 || summary.AverageBadAtK != 1 || summary.Slices["project_graph"].AverageRecallAtK != 1 {
		t.Fatalf("fragment-backed fact summary = %+v", summary)
	}
}

func TestScoreTracesDoesNotMapAmbiguousFragmentsToFactQRels(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1"}}
	cases := map[string]Case{"case-1": {CaseID: "case-1", Query: "which fact?", Limit: 1}}
	qrels := map[string]QRel{"case-1": {
		CaseID:       "case-1",
		RequiredRefs: []Ref{{Type: "fact", SourceDocID: "doc-multi:fact:2"}},
	}}
	traces := []RecallTrace{{
		CaseID:     "case-1",
		Query:      "which fact?",
		RankedRefs: []Ref{{Type: "fragment", ID: "fragment-multi"}},
	}}
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-multi", SourceDocID: "doc-multi"}, true)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-1", SourceDocID: "doc-multi"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-2", SourceDocID: "doc-multi"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-2", SourceDocID: "doc-multi:fact:2"}, false)

	scores, _, err := ScoreTraces("run-1", "candidate", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	if scores[0].RecallAtK != 0 || scores[0].FirstRequiredRank != 0 || len(scores[0].MissingRequired) != 1 {
		t.Fatalf("ambiguous fragment fact score = %+v", scores[0])
	}
}

func TestScoreTracesScoresContextEvidenceRefsSeparately(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"evidence"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "who owns alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredRefs:         []Ref{{Type: "fact", SourceDocID: "doc-fact"}},
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
			BadEvidenceRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-source-bad"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:              "case-1",
		Query:               "who owns alpha?",
		RankedRefs:          []Ref{{Type: "fact", ID: "fact-alpha"}},
		ContextRefs:         []Ref{{Type: "fact", ID: "fact-alpha"}},
		ContextEvidenceRefs: []Ref{{Type: "fragment", ID: "fragment-good"}, {Type: "fragment", ID: "fragment-bad"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-fact":        {Type: "fact", ID: "fact-alpha", SourceDocID: "doc-fact"},
		"doc-source-good": {Type: "fragment", ID: "fragment-good", SourceDocID: "doc-source-good"},
		"doc-source-bad":  {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-source-bad"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.ContextRecallAtK != 1 || score.ContextBadAtK != 0 {
		t.Fatalf("top-level score = %+v", score)
	}
	if !score.EvidenceScored || score.EvidenceRecallAtK != 1 || score.EvidenceBadAtK != 1 || score.EvidenceFirstRequiredRank != 1 || score.EvidenceFirstBadRank != 2 {
		t.Fatalf("evidence score = %+v", score)
	}
	if len(score.EvidenceBadRefsAtK) != 1 || len(score.EvidenceMissingRequired) != 0 {
		t.Fatalf("evidence refs = %+v", score)
	}
	if summary.EvidenceScoredCaseCount != 1 || summary.AverageEvidenceRecallAtK != 1 || summary.AverageEvidenceBadAtK != 1 || summary.EvidenceRequiredRank1Rate != 1 {
		t.Fatalf("evidence summary = %+v", summary)
	}
	if summary.Slices["evidence"].EvidenceScoredCaseCount != 1 || summary.Slices["evidence"].AverageEvidenceBadAtK != 1 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

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

func TestScoreTracesKeepsExplicitEmptyContextEvidenceRefs(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"evidence"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which source backs alpha?", Limit: 2},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-source-good"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:              "case-1",
		Query:               "which source backs alpha?",
		ContextRefs:         []Ref{{Type: "fragment", ID: "fragment-good"}},
		ContextEvidenceRefs: []Ref{},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-source-good": {Type: "fragment", ID: "fragment-good", SourceDocID: "doc-source-good"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if !score.EvidenceScored || score.EvidenceRecallAtK != 0 || len(score.EvidenceMissingRequired) != 1 {
		t.Fatalf("explicit empty evidence score = %+v; want scored miss without context fallback", score)
	}
	if summary.AverageEvidenceRecallAtK != 0 {
		t.Fatalf("explicit empty evidence summary = %+v; want no evidence recall", summary)
	}
}

func TestScoreTracesScoresDreamRefsSeparately(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1", Slices: []string{"dream_neighbor_hypothesis"}}}
	cases := map[string]Case{
		"case-1": {CaseID: "case-1", Query: "which hypothesis?", Limit: 2, IncludeDreams: true},
	}
	qrels := map[string]QRel{
		"case-1": {
			CaseID:            "case-1",
			RequiredRefs:      []Ref{{Type: "fact", SourceDocID: "doc-fact"}},
			RequiredDreamRefs: []Ref{{Type: "dream", SourceDocID: "dream-good"}},
			BadDreamRefs:      []Ref{{Type: "dream", SourceDocID: "dream-bad"}},
		},
	}
	traces := []RecallTrace{{
		CaseID:     "case-1",
		Query:      "which hypothesis?",
		RankedRefs: []Ref{{Type: "fact", ID: "fact-1"}},
		DreamRefs:  []Ref{{Type: "dream", ID: "dream-1"}, {Type: "dream", ID: "dream-bad-1"}},
	}}
	mapping := KnowledgeMapping{BySourceDocID: map[string]Ref{
		"doc-fact":   {Type: "fact", ID: "fact-1", SourceDocID: "doc-fact"},
		"dream-good": {Type: "dream", ID: "dream-1", SourceDocID: "dream-good"},
		"dream-bad":  {Type: "dream", ID: "dream-bad-1", SourceDocID: "dream-bad"},
	}}

	scores, summary, err := ScoreTraces("run-1", "baseline", "seed-1", "sha256:test", "suite.jsonl", suite, cases, qrels, traces, mapping)
	if err != nil {
		t.Fatalf("ScoreTraces: %v", err)
	}
	score := scores[0]
	if score.RecallAtK != 1 || score.BadAtK != 0 {
		t.Fatalf("memory score = %+v", score)
	}
	if !score.DreamScored || score.DreamRecallAtK != 1 || score.DreamBadAtK != 1 || score.DreamFirstRequiredRank != 1 || score.DreamFirstBadRank != 2 {
		t.Fatalf("dream score = %+v", score)
	}
	if summary.DreamScoredCaseCount != 1 || summary.AverageDreamRecallAtK != 1 || summary.AverageDreamBadAtK != 1 || summary.DreamRequiredRank1Rate != 1 {
		t.Fatalf("dream summary = %+v", summary)
	}
	if summary.Slices["dream_neighbor_hypothesis"].DreamScoredCaseCount != 1 || summary.Slices["dream_neighbor_hypothesis"].AverageDreamBadAtK != 1 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func writeEvalFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := SeedManifest{
		SchemaVersion: SeedSchemaVersion,
		SeedID:        "fixture",
		CorpusFile:    "corpus.jsonl",
		CasesFile:     "cases.jsonl",
		QrelsFile:     "qrels.jsonl",
	}
	if err := writeJSONFile(filepath.Join(dir, "seed_manifest.json"), manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "corpus.jsonl"), []CorpusItem{
		{SourceDocID: "doc-alpha", Content: "Alpha is the first Greek letter."},
		{SourceDocID: "doc-beta", Content: "Beta is the second Greek letter."},
	}); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "cases.jsonl"), []Case{
		{CaseID: "case-1", Query: "What is alpha?", Slices: []string{"direct"}, Limit: 2},
		{CaseID: "case-2", Query: "What is beta?", Slices: []string{"direct"}, Limit: 2},
	}); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "qrels.jsonl"), []QRel{
		{CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-alpha", Grade: 1}}},
		{CaseID: "case-2", RequiredRefs: []Ref{{Type: "fragment", SourceDocID: "doc-beta", Grade: 1}}},
	}); err != nil {
		t.Fatalf("write qrels: %v", err)
	}
	if err := writeJSONL(filepath.Join(dir, "suite.jsonl"), []SuiteCase{
		{CaseID: "case-1"},
		{CaseID: "case-2"},
	}); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	return dir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
