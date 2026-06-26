package evalharness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunValidateWritesArtifacts(t *testing.T) {
	dir := writeEvalFixture(t)
	out := filepath.Join(dir, "run")

	summary, err := Run(context.Background(), RunOptions{
		Mode:             "validate",
		SeedManifestPath: filepath.Join(dir, "seed_manifest.json"),
		SuitePath:        filepath.Join(dir, "suite.jsonl"),
		OutDir:           out,
		RunID:            "validate-test",
	})
	if err != nil {
		t.Fatalf("Run validate: %v", err)
	}
	if summary.CaseCount != 2 || summary.ScoredCaseCount != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	for _, name := range []string{"run_config.json", "seed_manifest.json", "suite.jsonl", "summary.json"} {
		if !fileExists(filepath.Join(out, name)) {
			t.Fatalf("missing artifact %s", name)
		}
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
	if scores[1].RelevantTotal != 1 || scores[1].RecallAtK != 0 || len(scores[1].UnmappedSourceRefs) != 1 {
		t.Fatalf("score[1] = %+v", scores[1])
	}
	if summary.AverageRecallAtK != 0.5 || summary.AverageBadAtK != 0.5 || summary.UnmappedSourceRefs != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Slices["direct"].CaseCount != 2 {
		t.Fatalf("slice summary = %+v", summary.Slices)
	}
}

func TestCompareSummariesRejectsSeedMismatch(t *testing.T) {
	_, err := CompareSummaries(Summary{SeedHash: "sha256:a"}, Summary{SeedHash: "sha256:b"})
	if err == nil {
		t.Fatal("CompareSummaries expected seed mismatch error")
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
