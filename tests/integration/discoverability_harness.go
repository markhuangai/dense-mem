package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// DiscoverabilityHarness wires the mock embedding server and helper factories
// used by UAT-1..UAT-15. Every method returns a deterministic fixture so red
// tests can be flipped to green without changing test bodies.
type DiscoverabilityHarness interface {
	MockEmbeddingURL() string
	MockEmbeddingToken() string
	SetEmbeddingBehavior(behavior EmbeddingBehavior)
	Reset()
}

// EmbeddingBehavior configures the mock embedding server response.
type EmbeddingBehavior struct {
	StatusCode int // 0 == success
	Delay      int // ms stall before reply
	Vector     []float32
	ErrorBody  string
}

// DefaultEmbeddingVectorSize is the default vector dimension for mock responses.
const DefaultEmbeddingVectorSize = 1536

// discoverabilityHarness is the concrete implementation of DiscoverabilityHarness.
type discoverabilityHarness struct {
	server      *httptest.Server
	token       string
	behavior    EmbeddingBehavior
	callCount   int
	callCountMu sync.Mutex
	t           *testing.T
}

// NewDiscoverabilityHarness creates a new DiscoverabilityHarness instance.
func NewDiscoverabilityHarness(t *testing.T) DiscoverabilityHarness {
	t.Helper()
	h := &discoverabilityHarness{
		t:     t,
		token: "test-embedding-token-12345",
		behavior: EmbeddingBehavior{
			StatusCode: 0,
			Delay:      0,
			Vector:     nil, // Will be generated deterministically from input
		},
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.handleEmbeddingRequest))
	return h
}

// MockEmbeddingURL returns the URL of the mock embedding server.
func (h *discoverabilityHarness) MockEmbeddingURL() string {
	return h.server.URL
}

// MockEmbeddingToken returns the mock API token for authentication.
func (h *discoverabilityHarness) MockEmbeddingToken() string {
	return h.token
}

// SetEmbeddingBehavior configures the mock server's response behavior.
func (h *discoverabilityHarness) SetEmbeddingBehavior(behavior EmbeddingBehavior) {
	h.behavior = behavior
}

// Reset restores default behavior and clears call counts.
func (h *discoverabilityHarness) Reset() {
	h.callCountMu.Lock()
	h.callCount = 0
	h.callCountMu.Unlock()
	h.behavior = EmbeddingBehavior{
		StatusCode: 0,
		Delay:      0,
		Vector:     nil,
	}
}

// CallCount returns the number of embedding requests received.
func (h *discoverabilityHarness) CallCount() int {
	h.callCountMu.Lock()
	defer h.callCountMu.Unlock()
	return h.callCount
}

// handleEmbeddingRequest handles incoming HTTP requests to the mock embedding endpoint.
func (h *discoverabilityHarness) handleEmbeddingRequest(w http.ResponseWriter, r *http.Request) {
	h.callCountMu.Lock()
	h.callCount++
	h.callCountMu.Unlock()

	// Verify Authorization header
	authHeader := r.Header.Get("Authorization")
	expectedAuth := "Bearer " + h.token
	if authHeader != expectedAuth {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Invalid authentication",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// Apply configured delay
	if h.behavior.Delay > 0 {
		time.Sleep(time.Duration(h.behavior.Delay) * time.Millisecond)
	}

	// Handle error responses
	if h.behavior.StatusCode > 0 {
		w.WriteHeader(h.behavior.StatusCode)
		if h.behavior.ErrorBody != "" {
			w.Write([]byte(h.behavior.ErrorBody))
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"message": fmt.Sprintf("Mock error %d", h.behavior.StatusCode),
					"type":    "server_error",
				},
			})
		}
		return
	}

	// Parse request body
	var req embeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Invalid request body",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// Generate deterministic embeddings
	embeddings := make([]embeddingData, len(req.Input))
	for i, input := range req.Input {
		vector := h.generateVector(input)
		embeddings[i] = embeddingData{
			Object:    "embedding",
			Index:     i,
			Embedding: vector,
		}
	}

	// Send successful response
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   embeddings,
		"model":  req.Model,
		"usage": map[string]int{
			"prompt_tokens": len(req.Input) * 10, // Mock token count
			"total_tokens":  len(req.Input) * 10,
		},
	})
}

// generateVector creates a deterministic embedding vector from input text.
func (h *discoverabilityHarness) generateVector(input string) []float32 {
	// Use pre-configured vector if provided
	if h.behavior.Vector != nil {
		return h.behavior.Vector
	}

	// Generate deterministic vector from SHA-256 hash of input
	hash := sha256.Sum256([]byte(input))
	hashStr := hex.EncodeToString(hash[:])

	vector := make([]float32, DefaultEmbeddingVectorSize)
	for i := 0; i < DefaultEmbeddingVectorSize; i++ {
		// Use hash bytes cyclically to generate normalized float values
		byteIdx := i % len(hashStr)
		// Normalize to range [-1, 1] based on byte value
		vector[i] = float32(hashStr[byteIdx])/128.0 - 1.0
	}

	return vector
}

// embeddingRequest represents an OpenAI-style embedding API request.
type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

// embeddingData represents a single embedding in the response.
type embeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}
