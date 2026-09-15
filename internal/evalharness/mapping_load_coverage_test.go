package evalharness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluationMappingMergeAndResolutionBranches(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{SourceDocID: "doc", Type: "evidence", ID: "e-1"}, false)
	addSourceMapping(&mapping, Ref{SourceDocID: "doc", Type: "evidence", ID: "e-1"}, false)
	addSourceMapping(&mapping, Ref{}, false)
	addDreamSourceRefs(&mapping, "dream", []Ref{{Type: "evidence", ID: "e-1"}, {Type: "", ID: "ignored"}})
	if _, ok := resolveSourceMapping(mapping, "doc", "evidence"); !ok {
		t.Fatal("source mapping was not resolved")
	}
	if _, ok := resolveSourceMapping(mapping, "doc", "missing"); ok {
		t.Fatal("missing source mapping was resolved")
	}
	filtered := newKnowledgeMapping()
	mergeFilteredKnowledgeMapping(&filtered, mapping, map[string]struct{}{"doc": {}})
	if _, ok := filtered.BySourceDocID["doc"]; !ok {
		t.Fatal("filtered mapping dropped retained source")
	}
	mergeKnowledgeMapping(&filtered, mapping)
	if len(filtered.DreamSourceRefsByID["dream"]) != 1 {
		t.Fatal("dream source mapping was not merged")
	}
	cycles := expectedDreamCycleSeeds(mapping, []ExpectedDream{{Hypothesis: "h", SourceRefs: []Ref{{Type: "evidence", ID: "e-1"}}}})
	if len(cycles) != 1 || cycles[0].Hypothesis != "h" {
		t.Fatalf("dream cycle seeds = %#v", cycles)
	}
}

func TestEvaluationLoadHelpersHashAndJSONLBoundaries(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"schema_version":"dense-mem.eval.seed.v1","seed_id":"seed","corpus_file":"corpus.jsonl","cases_file":"cases.jsonl","qrels_file":"qrels.jsonl"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"corpus.jsonl": "{\"source_doc_id\":\"doc\",\"content\":\"text\"}\n", "cases.jsonl": "{\"case_id\":\"case\",\"query\":\"query\"}\n", "qrels.jsonl": "{\"case_id\":\"case\",\"required_refs\":[{\"type\":\"source_doc\",\"source_doc_id\":\"doc\"}]}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := LoadSeedManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SeedHash(manifestPath, manifest); err != nil {
		t.Fatalf("SeedHash = %v", err)
	}
	if got := resolveSeedPath(manifestPath, "/tmp/absolute"); got != "/tmp/absolute" {
		t.Fatalf("absolute seed path = %q", got)
	}
	if got := IndexCases([]Case{{CaseID: "case", Query: "query"}})["case"].Query; got != "query" {
		t.Fatalf("case index = %q", got)
	}
	if got := IndexQrels([]QRel{{CaseID: "case"}})["case"].CaseID; got != "case" {
		t.Fatalf("qrel index = %q", got)
	}
	bad := filepath.Join(root, "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var rows []Case
	if err := readJSONL(bad, &rows); err == nil {
		t.Fatal("malformed JSONL was accepted")
	}
}
