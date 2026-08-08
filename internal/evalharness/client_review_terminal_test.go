package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestHTTPClientImportCorpusRejectsQuarantinedSubmissionEvenWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-quarantined"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-quarantined",
				"processing_state": "quarantined",
				"evidence": []map[string]any{{
					"evidence_id": "evidence-quarantined",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-quarantined", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "submission submission-quarantined quarantined") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}

func TestHTTPClientImportCorpusDoesNotAcceptAwaitingReviewWithEvidence(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "tool:remember":
			_ = json.NewEncoder(w).Encode(map[string]any{"submission_id": "submission-awaiting-review"})
		case "tool:get_submission_status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"submission_id":    "submission-awaiting-review",
				"processing_state": "awaiting_review",
				"evidence": []map[string]any{{
					"evidence_id": "evidence-awaiting-review",
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", PlacementTimeout: 20 * time.Millisecond, Client: server.Client()}
	_, err := client.ImportCorpus(context.Background(), []CorpusItem{{SourceDocID: "doc-awaiting-review", Content: "content"}})
	if err == nil || !strings.Contains(err.Error(), "did not complete within") {
		t.Fatalf("ImportCorpus err = %v", err)
	}
}
