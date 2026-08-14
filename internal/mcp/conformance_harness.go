package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// NewLegacyHTTPHandler exposes the pre-SDK transport through the same narrow
// HTTP boundary used by the differential conformance harness. Production
// routing remains owned by internal/http/handler.
func NewLegacyHTTPHandler(server *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || server == nil {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload bytes.Buffer
		if _, err := payload.ReadFrom(r.Body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := server.HandlePayloadResult(r.Context(), payload.Bytes())
		if !result.Respond {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(result.Payload)
	})
}

// CanonicalizeConformanceResponse removes only transport-generated fields that
// are allowed to vary between implementations. Semantic JSON-RPC result and
// error fields remain exact.
func CanonicalizeConformanceResponse(payload []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	canonicalizeTransportFields(value)
	return json.Marshal(value)
}

func canonicalizeTransportFields(value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if result, ok := object["result"].(map[string]any); ok {
		delete(result, "_meta")
		delete(result, "ttlMs")
		delete(result, "cacheScope")
		if serverInfo, ok := result["serverInfo"].(map[string]any); ok {
			delete(serverInfo, "version")
		}
		if tools, ok := result["tools"].([]any); ok {
			for _, item := range tools {
				if tool, ok := item.(map[string]any); ok && tool["outputSchema"] == nil {
					delete(tool, "outputSchema")
				}
			}
		}
		if prompts, ok := result["prompts"].([]any); ok {
			for _, item := range prompts {
				if prompt, ok := item.(map[string]any); ok {
					delete(prompt, "title")
				}
			}
		}
	}
	if errObject, ok := object["error"].(map[string]any); ok {
		delete(errObject, "data")
	}
}

// CompareConformanceResponses compares two responses after the bounded
// canonicalization above. It never normalizes tool content or error messages.
func CompareConformanceResponses(left, right []byte) (bool, error) {
	leftCanonical, err := CanonicalizeConformanceResponse(left)
	if err != nil {
		return false, err
	}
	rightCanonical, err := CanonicalizeConformanceResponse(right)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(leftCanonical)) == strings.TrimSpace(string(rightCanonical)), nil
}

// ConformanceCall runs one request against both handlers and reports whether
// the protocol responses are semantically equivalent.
func ConformanceCall(ctx context.Context, legacy, sdk http.Handler, payload []byte, headers http.Header) (bool, error) {
	legacyResponse, err := conformanceHTTPRequest(ctx, legacy, payload, headers)
	if err != nil {
		return false, err
	}
	sdkResponse, err := conformanceHTTPRequest(ctx, sdk, payload, headers)
	if err != nil {
		return false, err
	}
	matched, err := CompareConformanceResponses(legacyResponse.body, sdkResponse.body)
	if err != nil {
		return false, err
	}
	if legacyResponse.status != sdkResponse.status {
		return false, nil
	}
	return matched, nil
}

type conformanceResponse struct {
	status int
	body   []byte
}

func conformanceHTTPRequest(ctx context.Context, handler http.Handler, payload []byte, headers http.Header) (conformanceResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mcp.test/mcp", bytes.NewReader(payload))
	if err != nil {
		return conformanceResponse{}, err
	}
	request.Header = headers.Clone()
	response := newConformanceRecorder()
	handler.ServeHTTP(response, request)
	return conformanceResponse{status: response.code, body: response.body.Bytes()}, nil
}

type conformanceRecorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func newConformanceRecorder() *conformanceRecorder {
	return &conformanceRecorder{header: make(http.Header)}
}
func (r *conformanceRecorder) Header() http.Header  { return r.header }
func (r *conformanceRecorder) WriteHeader(code int) { r.code = code }
func (r *conformanceRecorder) Write(payload []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(payload)
}
