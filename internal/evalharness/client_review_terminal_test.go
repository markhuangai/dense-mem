package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestHTTPClientImportCorpusAllowsReviewTerminalWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-review-terminal"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-review-terminal",
				"processing_state": "rejected",
				"search_state":     "not_required",
				"evidence": []map[string]any{{
					"evidence_id": "evidence-review-terminal",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	mapping, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-review-terminal", Content: "content"}})
	if err != nil {
		t.Fatalf("ImportCorpus: %v", err)
	}
	ref, ok := mapping.BySourceDocID["doc-review-terminal"]
	if !ok || ref.Type != "fragment" || ref.ID != "evidence-review-terminal" {
		t.Fatalf("source mapping = %+v, %v", ref, ok)
	}
}
