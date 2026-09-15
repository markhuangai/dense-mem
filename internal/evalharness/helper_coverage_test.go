package evalharness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEvaluationOutputHelpersCoverStateAndReferenceVariants(t *testing.T) {
	if submissionProcessingState(map[string]any{"processing_state": " completed "}) != "completed" {
		t.Fatal("processing state was not normalized")
	}
	for _, test := range []struct {
		left, right, want string
	}{
		{"", "current", "current"},
		{"current", "pending", "pending"},
		{"pending", "failed", "failed"},
		{"not_required", "", "not_required"},
		{"unknown", "other", "unknown"},
	} {
		if got := combineSubmissionSearchState(test.left, test.right); got != test.want {
			t.Errorf("combine(%q,%q) = %q, want %q", test.left, test.right, got, test.want)
		}
	}
	if got := submissionSearchState(map[string]any{"search_state": "current"}); got != "current" {
		t.Fatalf("direct search state = %q", got)
	}
	if got := submissionSearchState(map[string]any{"evidence": []any{
		map[string]any{"search_state": "not_required"}, map[string]any{"search_state": "pending"},
	}}); got != "pending" {
		t.Fatalf("nested search state = %q", got)
	}
	if got := submissionErrorMessage(map[string]any{"errors": []any{map[string]any{"message": "message"}}}); got != "message" {
		t.Fatalf("top-level error message = %q", got)
	}
	if got := submissionErrorMessage(map[string]any{"errors": []any{map[string]any{"code": "CODE"}}}); got != "CODE" {
		t.Fatalf("top-level error code = %q", got)
	}
	if got := submissionErrorMessage(map[string]any{"evidence": []any{map[string]any{"errors": []any{map[string]any{"message": "nested"}}}}}); got != "nested" {
		t.Fatalf("nested error message = %q", got)
	}
	if got := submissionErrorMessage(map[string]any{"evidence": []any{map[string]any{"errors": []any{map[string]any{"code": "NESTED_CODE"}}}}}); got != "NESTED_CODE" {
		t.Fatalf("nested error code = %q", got)
	}
	if submissionErrorMessage(map[string]any{}) != "" {
		t.Fatal("missing error returned a value")
	}

	if got := strings.Join(stringsFromAny([]string{"a", "a", " b "}), ","); got != "a,b" {
		t.Fatalf("string slice conversion = %q", got)
	}
	if got := strings.Join(stringsFromAny([]any{"a", json.Number("2"), 3}), ","); got != "2,a" {
		t.Fatalf("any slice conversion = %q", got)
	}
	if stringsFromAny(3) != nil {
		t.Fatal("scalar converted to strings")
	}
	refs := sourceRefsFromAny([]any{map[string]any{"type": "fragment", "id": "f1"}, map[string]any{"type": "", "id": "f2"}, "invalid"})
	if len(refs) != 1 || refs[0].ID != "f1" {
		t.Fatalf("source refs = %#v", refs)
	}
	item := map[string]any{
		"id": "fallback", "fragment_id": "fragment", "claim_id": "claim", "metadata": map[string]any{"source_doc_id": "doc"},
		"source_refs": []any{map[string]any{"type": "fragment", "id": "f1"}},
	}
	if knowledgeItemID("fragment", item) != "fragment" || knowledgeItemID("unknown", item) != "fallback" {
		t.Fatal("knowledge item IDs were not selected")
	}
	if got := sourceDocIDsFromKnowledgeItem("hypothesis", item, map[string][]string{"f1": {"doc-ref"}}); len(got) != 2 {
		t.Fatalf("source document IDs = %#v", got)
	}
	if evidenceIDFromSubmission(map[string]any{"evidence": []any{map[string]any{"fragment_id": "fragment"}}}) != "fragment" {
		t.Fatal("evidence ID fallback failed")
	}
	trace := traceFromToolOutput(Case{CaseID: "case", Query: "fallback"}, map[string]any{
		"ranked_refs": []any{map[string]any{"type": "claim", "id": "c", "rank": json.Number("2")}}, "latency_ms": json.Number("3"),
	})
	if trace.Query != "fallback" || len(trace.RankedRefs) != 1 || trace.LatencyMS != 3 || trace.RankedRefs[0].Rank != 2 {
		t.Fatalf("trace output = %#v", trace)
	}
}

func TestEvaluationContractAndRetryHelpers(t *testing.T) {
	for _, schema := range []map[string]any{
		{"properties": map[string]any{"contract_version": map[string]any{"enum": []any{domain.PreviousContractVersion, domain.ContractVersion}}}},
		{"properties": map[string]any{"contract_version": map[string]any{"enum": []string{domain.PreviousContractVersion}}}},
		{"oneOf": []any{map[string]any{"properties": map[string]any{"contract_version": map[string]any{"enum": []any{domain.ContractVersion}}}}}},
	} {
		if contractVersionFromSchema(schema) == "" {
			t.Fatalf("contract version not found in %#v", schema)
		}
	}
	if contractVersionFromSchema(map[string]any{"oneOf": []any{map[string]any{"properties": map[string]any{"contract_version": map[string]any{"enum": []any{domain.ContractVersion}}}}, map[string]any{"properties": map[string]any{"contract_version": map[string]any{"enum": []any{domain.PreviousContractVersion}}}}}}) != "" {
		t.Fatal("mixed contract versions were accepted")
	}
	if preferredContractVersion([]any{"unknown"}) != "" || preferredContractVersion([]any{domain.PreviousContractVersion}) != domain.PreviousContractVersion {
		t.Fatal("contract version preference mismatch")
	}
	if !structuredToolErrorRetryable(map[string]any{"retryable": true, "next_action": "retry_same_request"}) {
		t.Fatal("top-level retry marker was ignored")
	}
	if !structuredToolErrorRetryable(map[string]any{"errors": []any{map[string]any{"retryable": true, "next_action": "retry_same_request"}}}) {
		t.Fatal("nested retry marker was ignored")
	}
	if structuredToolErrorRetryable(map[string]any{"retryable": true, "next_action": "other"}) {
		t.Fatal("invalid retry marker was accepted")
	}
	if isTransientCallToolError(errors.New("ordinary")) {
		t.Fatal("ordinary error was treated as transient")
	}
}

func TestSubmissionPlacementAndScoringHelpers(t *testing.T) {
	if submissionSearchState(map[string]any{"evidence": []any{map[string]any{"search_state": "current"}, map[string]any{"search_state": "failed"}}}) != "failed" {
		t.Fatal("failed evidence did not dominate")
	}
	if got := combineSubmissionSearchState(" ", " "); got != "" {
		t.Fatalf("empty state = %q", got)
	}
	if got := endpoint("http://example/", "/mcp"); got != "http://example/mcp" {
		t.Fatalf("endpoint = %q", got)
	}
	if nestedString(map[string]any{"metadata": map[string]any{"source": "value"}}, "metadata", "source") != "value" {
		t.Fatal("nested string failed")
	}
	if nestedStringPath(map[string]any{"a": map[string]any{"b": "value"}}, "a", "b") != "value" {
		t.Fatal("nested string path failed")
	}
	if _, ok := nestedValue(map[string]any{}, "missing", "value"); ok {
		t.Fatal("missing nested value reported present")
	}
	if intValue(json.Number("4")) != 4 || int64Value(json.Number("5")) != 5 || stringValue(json.Number("6")) != "6" {
		t.Fatal("JSON helper conversions failed")
	}
	if firstNonEmpty(" ", "value") != "value" {
		t.Fatal("firstNonEmpty failed")
	}
	score := ScoreTrace("case", 1, QRel{RequiredRefs: []Ref{{Type: "claim", ID: "claim-1"}}}, RecallTrace{
		CaseID:     "case",
		RankedRefs: []Ref{{Type: "claim", ID: "claim-1"}},
	}, KnowledgeMapping{})
	if score.CaseID != "case" || score.RelevantAtK != 1 {
		t.Fatalf("score = %#v", score)
	}
}

func TestHTTPClientCorpusAndOutputHelperBranches(t *testing.T) {
	if (&HTTPStatusError{Method: "GET", URL: "https://example", StatusCode: 400, Body: "bad"}).Error() != "GET https://example returned 400: bad" {
		t.Fatal("HTTP status error was not formatted")
	}
	if (&StructuredToolError{Tool: "remember", Result: map[string]any{"processing_state": "failed"}}).Error() == "" || (*StructuredToolError)(nil).Error() == "" {
		t.Fatal("structured tool error was empty")
	}
	if got := corpusImportGroupKey(CorpusItem{Metadata: map[string]any{"case_id": " case-1 "}, SourceDocID: "doc"}); got != "case:case-1" {
		t.Fatalf("case import key = %q", got)
	}
	if got := corpusImportGroupKey(CorpusItem{Content: "content"}); got != "item:content" {
		t.Fatalf("content import key = %q", got)
	}
	if got := knowledgeItemID("fragment", map[string]any{"fragment_id": "fragment"}); got != "fragment" || knowledgeItemID("unknown", map[string]any{"id": "fallback"}) != "fallback" {
		t.Fatal("knowledge item ID fallback failed")
	}
	if got := evidenceIDFromSubmission(map[string]any{"evidence": []any{map[string]any{"id": "evidence"}}}); got != "evidence" || evidenceIDFromSubmission(map[string]any{}) != "" {
		t.Fatal("evidence ID extraction failed")
	}
	if got := nestedStringPath(map[string]any{"a": map[string]any{"b": "value"}}, "a", "missing"); got != "" || nestedStringPath(map[string]any{}, "a") != "" {
		t.Fatal("nested string missing path returned a value")
	}
	root := t.TempDir()
	path := filepath.Join(root, "corpus.jsonl")
	if err := os.WriteFile(path, []byte("# comment\n\n{\"source_doc_id\":\"doc\",\"content\":\"content\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := scanCorpusFile(path, func(CorpusItem) error { count++; return nil }); err != nil || count != 1 {
		t.Fatalf("scanCorpusFile = %d, %v", count, err)
	}
	if _, err := decodeCorpusItem([]byte(`{"claims":[]}`)); err == nil {
		t.Fatal("legacy claims field was accepted")
	}
	if _, err := decodeCorpusItem([]byte("{")); err == nil {
		t.Fatal("malformed corpus item was accepted")
	}
}
