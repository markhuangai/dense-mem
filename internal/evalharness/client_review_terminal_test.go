package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPClientImportCorpusRejectsSemanticRejectionEvenWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": "submission-review-terminal", "submission_kind": "remember",
			"processing_state": "rejected", "search_state": "not_required", "correlation_id": "correlation-review-terminal",
			"evidence":             []map[string]any{{"disposition": "not_stored", "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "not_required"}},
			"relationship_results": []map[string]any{}, "errors": []map[string]any{{"code": "no_supported_memory", "message": "no supported memory"}},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-review-terminal", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "import doc-review-terminal: remember processing_state rejected: no supported memory") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusRejectsQuarantinedSubmissionEvenWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": "submission-quarantined", "submission_kind": "remember",
			"processing_state": "quarantined", "search_state": "not_required", "correlation_id": "correlation-quarantined",
			"evidence":             []map[string]any{{"disposition": "not_stored", "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "not_required"}},
			"relationship_results": []map[string]any{}, "errors": []map[string]any{{"code": "submission_quarantined", "message": "submission quarantined"}},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-quarantined", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "import doc-quarantined: remember processing_state quarantined") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusRejectsUnknownProcessingStateWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": "submission-unknown-state", "submission_kind": "remember",
			"processing_state": "unknown_state", "search_state": "not_required", "correlation_id": "correlation-unknown-state",
			"evidence": []map[string]any{}, "relationship_results": []map[string]any{}, "errors": []map[string]any{},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-awaiting-review", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), `import doc-awaiting-review: remember processing_state unknown_state`) {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}
