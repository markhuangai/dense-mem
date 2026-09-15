package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPClientCallToolBoundaryResponses(t *testing.T) {
	if got := (&StructuredToolError{Tool: "tool", Result: map[string]any{}}).Error(); !strings.Contains(got, "structured tool error") {
		t.Fatalf("default structured error = %q", got)
	}
	client := &HTTPClient{}
	if err := client.CallTool(context.Background(), "tool", map[string]any{}, nil); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatal("missing base URL was accepted")
	}
	client.BaseURL = "http://example.test"
	if err := client.CallTool(context.Background(), "tool", map[string]any{}, nil); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatal("missing API key was accepted")
	}

	responses := []struct {
		name string
		body any
		want string
	}{
		{"rpc error", map[string]any{"error": map[string]any{"code": -1, "message": "bad request"}}, "returned -1"},
		{"is error without structure", map[string]any{"result": map[string]any{"isError": true}}, "without structuredContent"},
		{"is error structured", map[string]any{"result": map[string]any{"isError": true, "structuredContent": map[string]any{"errors": []any{map[string]any{"message": "rejected"}}}}}, "structured tool error"},
		{"no json", map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "image", "text": ""}}}}, "no JSON text"},
		{"bad json", map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": "{"}}}}, "decode mcp"},
	}
	for _, tc := range responses {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
				// Rewrite the response with the case-specific payload after the
				// header has been validated.
			}))
			server.Close()
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
				}
				payload := map[string]any{"jsonrpc": "2.0", "id": 1}
				for key, value := range tc.body.(map[string]any) {
					payload[key] = value
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer server.Close()
			client := &HTTPClient{BaseURL: server.URL, APIKey: "key", Client: server.Client()}
			var out map[string]any
			err := client.CallTool(context.Background(), "tool", map[string]any{"input": "value"}, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CallTool error = %v, want %q", err, tc.want)
			}
		})
	}
	// Structured content is valid JSON, but the caller's output type can still
	// reject it during unmarshalling.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{"isError": true, "structuredContent": map[string]any{"error": "bad"}}})
	}))
	var incompatible int
	err := (&HTTPClient{BaseURL: server.URL, APIKey: "key", Client: server.Client()}).CallTool(context.Background(), "tool", map[string]any{}, &incompatible)
	server.Close()
	if err == nil || !strings.Contains(err.Error(), "decode mcp tools/call tool structured error") {
		t.Fatalf("incompatible structured output error = %v", err)
	}
	if err := (&HTTPClient{BaseURL: "http://example.test", APIKey: "key"}).callMCPTool(context.Background(), "tool", 1, nil); err == nil || !strings.Contains(err.Error(), "arguments must be an object") {
		t.Fatal("scalar tool arguments were accepted")
	}
	if err := (&HTTPClient{BaseURL: "http://example.test", APIKey: "key"}).callMCPTool(context.Background(), "tool", make(chan int), nil); err == nil {
		t.Fatal("unmarshalable tool arguments were accepted")
	}
	root := t.TempDir()
	corpusPath := filepath.Join(root, "corpus.jsonl")
	if err := os.WriteFile(corpusPath, []byte(`{"source_doc_id":"doc","content":"content"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanCorpusFile(corpusPath, func(CorpusItem) error { return errors.New("callback failed") }); err == nil || !strings.Contains(err.Error(), "callback failed") {
		t.Fatalf("scan callback error = %v", err)
	}
	if _, err := decodeCorpusItem([]byte(`{"source_doc_id":1,"content":"content"}`)); err == nil {
		t.Fatal("invalid corpus field type was accepted")
	}
	if got := nestedStringPath(map[string]any{}, ""); got != "" || nestedStringPath(nil) != "" {
		t.Fatal("empty nested path was not empty")
	}
	if got := knowledgeItemID("claim", map[string]any{"claim_id": "claim"}); got != "claim" || knowledgeItemID("fact", map[string]any{"fact_id": "fact"}) != "fact" {
		t.Fatalf("knowledge item IDs = %q/%q", got, knowledgeItemID("fact", map[string]any{"fact_id": "fact"}))
	}
	if refs := sourceRefsFromAny([]any{"bad", map[string]any{"type": "evidence", "id": "e"}}); len(refs) != 1 {
		t.Fatalf("source refs = %#v", refs)
	}
}
