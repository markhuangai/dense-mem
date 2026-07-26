package evalharness

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newEvalHarnessServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			handler.ServeHTTP(w, r)
			return
		}
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method != "tools/call" || req.Params.Name == "" {
			http.Error(w, "invalid mcp tools/call request", http.StatusBadRequest)
			return
		}
		args := req.Params.Arguments
		if len(args) == 0 {
			args = []byte("{}")
		}
		toolReq := r.Clone(r.Context())
		toolReq.URL.Path = "tool:" + req.Params.Name
		toolReq.RequestURI = ""
		toolReq.Body = io.NopCloser(bytes.NewReader(args))
		toolReq.ContentLength = int64(len(args))

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, toolReq)
		if rec.Code < 200 || rec.Code >= 300 {
			for key, values := range rec.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []map[string]string{{
					"type": "text",
					"text": rec.Body.String(),
				}},
			},
		})
	}))
}
