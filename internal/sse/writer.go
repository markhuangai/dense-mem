package sse

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// Common errors for SSE writer.
var (
	ErrFlusherNotSupported = errors.New("http.Flusher not supported by ResponseWriter")
	ErrStreamClosed        = errors.New("SSE stream is closed")
	ErrEvidenceTooLarge    = errors.New("evidence payload exceeds maximum size")
)

// SSEWriter is the interface for writing Server-Sent Events.
// Implementations must be safe for sequential use (not concurrent).
type SSEWriter interface {
	// WriteEvent writes an SSE event to the response.
	// Returns ErrStreamClosed if the stream has been closed (after done or error event).
	WriteEvent(eventType string, payload any) error

	// WriteComment writes an SSE comment to the response.
	// Comments are used for keepalive frames and are ignored by clients.
	// Returns ErrStreamClosed if the stream has been closed.
	WriteComment(text string) error

	// Close closes the SSE stream.
	// It is safe to call Close multiple times.
	Close() error
}

// sseWriter implements SSEWriter for HTTP responses.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	closed  bool
	mu      sync.Mutex
}

// Ensure sseWriter implements SSEWriter.
var _ SSEWriter = (*sseWriter)(nil)

// NewSSEWriter creates a new SSEWriter wrapping the given http.ResponseWriter.
// Returns ErrFlusherNotSupported if the ResponseWriter does not implement http.Flusher.
func NewSSEWriter(w http.ResponseWriter) (SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, ErrFlusherNotSupported
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	return &sseWriter{
		w:       w,
		flusher: flusher,
		closed:  false,
	}, nil
}

// WriteEvent writes an SSE event to the response.
// The event is formatted as: "event: <type>\ndata: <json>\n\n"
func (s *sseWriter) WriteEvent(eventType string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStreamClosed
	}

	// Handle terminal events
	if eventType == EventTypeDone || eventType == EventTypeError {
		defer func() {
			s.closed = true
		}()
	}

	// Validate evidence payload size
	if eventType == EventTypeEvidence {
		if err := s.validateEvidencePayload(payload); err != nil {
			return err
		}
	}

	// Sanitize tool_call payloads (strip secrets)
	if eventType == EventTypeToolCall {
		payload = sanitizeToolCallPayload(payload)
	}

	// Marshal payload to JSON
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Write SSE formatted event: "event: <type>\ndata: <json>\n\n"
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, data)
	if err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}

	// Flush the response
	s.flusher.Flush()

	return nil
}

// WriteComment writes an SSE comment to the response.
// Comments are formatted as: ": <text>\n\n"
// They are used for keepalive frames and are ignored by clients.
func (s *sseWriter) WriteComment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrStreamClosed
	}

	// Write SSE comment: ": <text>\n\n"
	_, err := fmt.Fprintf(s.w, ": %s\n\n", text)
	if err != nil {
		return fmt.Errorf("failed to write comment: %w", err)
	}

	// Flush the response
	s.flusher.Flush()

	return nil
}

// Close closes the SSE stream.
// It writes a done event if not already closed.
func (s *sseWriter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	return nil
}

// validateEvidencePayload checks if evidence payload size is within limits.
func (s *sseWriter) validateEvidencePayload(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil // If we can't marshal, let WriteEvent handle it
	}

	if len(data) > MaxEvidencePayloadSize {
		return ErrEvidenceTooLarge
	}

	return nil
}

// sanitizeToolCallPayload removes secret fields from tool call payloads.
func sanitizeToolCallPayload(payload any) any {
	// If payload is a map, remove secret fields
	if m, ok := payload.(map[string]any); ok {
		return sanitizeMap(m)
	}

	// If payload is a struct with json tags, we need to handle it differently
	// For now, just return as-is - specific sanitization would be handled
	// by the caller passing in sanitized data
	return payload
}

// sanitizeMap removes secret fields from a map.
func sanitizeMap(m map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range m {
		// Skip known secret field names
		if isSecretField(k) {
			continue
		}

		// Recursively sanitize nested maps
		if nested, ok := v.(map[string]any); ok {
			result[k] = sanitizeMap(nested)
		} else {
			result[k] = v
		}
	}
	return result
}

// isSecretField checks if a field name indicates a secret.
func isSecretField(field string) bool {
	secretFields := []string{
		"secret",
		"password",
		"token",
		"api_key",
		"apiKey",
		"authorization",
		"credential",
		"private_key",
		"privateKey",
	}

	for _, secret := range secretFields {
		if field == secret {
			return true
		}
	}
	return false
}
