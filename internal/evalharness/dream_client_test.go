package evalharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientRunsDreamCycleAndExportsDreamMapping(t *testing.T) {
	var runInput map[string]any
	var listInput map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tools/eval_run_dream_cycle":
			if err := json.NewDecoder(r.Body).Decode(&runInput); err != nil {
				t.Fatalf("decode run dream cycle input: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run-1"})
		case "/api/v1/tools/eval_list_knowledge_refs":
			if err := json.NewDecoder(r.Body).Decode(&listInput); err != nil {
				t.Fatalf("decode list dreams input: %v", err)
			}
			if listInput["type"] != "dream" || listInput["metadata_only"] != false {
				t.Fatalf("list dream input = %#v", listInput)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{
					"dream_id": "dream-1",
					"source_refs": []map[string]any{
						{"type": "fact", "id": "fact-employer"},
						{"type": "fact", "id": "fact-location"},
					},
				}},
				"next_cursor": "",
				"has_more":    false,
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := &HTTPClient{BaseURL: server.URL, APIKey: "api-key", Client: server.Client()}
	if err := client.RunDreamCycle(context.Background(), 2); err != nil {
		t.Fatalf("RunDreamCycle: %v", err)
	}
	if runInput["manual"] != true || runInput["dream_enabled"] != true || runInput["max_outputs"] != float64(2) {
		t.Fatalf("run dream cycle input = %#v", runInput)
	}

	mapping, err := client.ExportDreamMapping(context.Background(), 25)
	if err != nil {
		t.Fatalf("ExportDreamMapping: %v", err)
	}
	refs := mapping.DreamSourceRefsByID["dream-1"]
	if len(refs) != 2 || refs[0].ID != "fact-employer" || refs[1].ID != "fact-location" {
		t.Fatalf("dream source refs = %+v", refs)
	}
	if listInput["limit"] != float64(25) {
		t.Fatalf("list limit = %#v", listInput["limit"])
	}
	if err := client.RunDreamCycle(context.Background(), 0); err != nil {
		t.Fatalf("RunDreamCycle zero expected dreams: %v", err)
	}
}
