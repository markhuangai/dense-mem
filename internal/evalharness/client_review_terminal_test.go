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
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id": "submission-review-terminal", "processing_state": "rejected", "search_state": "not_required",
				"evidence": []map[string]any{{"disposition": "not_stored", "evidence_index": 0, "search_state": "not_required"}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-review-terminal", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "import doc-review-terminal: remember processing_state rejected") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusRejectsQuarantinedSubmissionEvenWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id": "submission-quarantined", "processing_state": "quarantined", "search_state": "not_required",
				"evidence": []map[string]any{{"disposition": "not_stored", "evidence_index": 0, "search_state": "not_required"}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-quarantined", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "remember processing_state quarantined") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusRejectsUnknownProcessingStateWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-unknown-state", "processing_state": "unknown_state"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-awaiting-review", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "remember processing_state unknown_state") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}
