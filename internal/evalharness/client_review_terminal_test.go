package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPClientImportCorpusRejectsPolicyFailureEvenWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dense-mem.v2.6.2", "submission_id": "submission-review-terminal", "submission_kind": "remember",
			"processing_state": "failed", "search_state": "not_required", "correlation_id": "correlation-policy-failure",
			"evidence":             []map[string]any{{"disposition": "not_stored", "evidence_index": 0, "superseded_evidence_ids": []string{}, "search_state": "not_required", "reason": "submission_policy_rejected"}},
			"relationship_results": []map[string]any{}, "errors": []map[string]any{{"code": "submission_policy_rejected", "message": "submission was rejected by semantic policy"}},
		})
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-review-terminal", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "import doc-review-terminal: remember processing_state failed: submission was rejected by semantic policy") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusRejectsUnknownProcessingStateWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "tool:remember" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"contract_version": "dense-mem.v2.6.2", "submission_id": "submission-unknown-state", "submission_kind": "remember",
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
