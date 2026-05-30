package sse

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockFlusher is a mock http.ResponseWriter that implements http.Flusher.
type mockFlusher struct {
	*httptest.ResponseRecorder
	flushed bool
}

func newMockFlusher() *mockFlusher {
	return &mockFlusher{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          false,
	}
}

func (m *mockFlusher) Flush() {
	m.flushed = true
}

// mockNonFlusher is a custom type that embeds a ResponseWriter but does NOT implement Flusher.
type mockNonFlusher struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

func newMockNonFlusher() *mockNonFlusher {
	return &mockNonFlusher{
		header: make(http.Header),
	}
}

func (m *mockNonFlusher) Header() http.Header {
	return m.header
}

func (m *mockNonFlusher) Write(b []byte) (int, error) {
	return m.body.Write(b)
}

func (m *mockNonFlusher) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

type errorFlusher struct {
	header http.Header
}

func (e *errorFlusher) Header() http.Header {
	if e.header == nil {
		e.header = make(http.Header)
	}
	return e.header
}

func (e *errorFlusher) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (e *errorFlusher) WriteHeader(int) {}
func (e *errorFlusher) Flush()          {}

// TestSSEEventFormat tests that SSE events are formatted correctly as "event: <type>\ndata: <json>\n\n".
func TestSSEEventFormat(t *testing.T) {
	tests := []struct {
		name       string
		eventType  string
		payload    any
		wantEvent  string
		wantClosed bool
	}{
		{
			name:      "tool_call event",
			eventType: EventTypeToolCall,
			payload:   map[string]any{"name": "test_tool", "args": map[string]any{"query": "hello"}},
			wantEvent: "tool_call",
		},
		{
			name:      "text_delta event",
			eventType: EventTypeTextDelta,
			payload:   map[string]any{"delta": "Hello, world!"},
			wantEvent: "text_delta",
		},
		{
			name:      "evidence event",
			eventType: EventTypeEvidence,
			payload:   map[string]any{"content": "some evidence"},
			wantEvent: "evidence",
		},
		{
			name:       "done event",
			eventType:  EventTypeDone,
			payload:    map[string]any{},
			wantEvent:  "done",
			wantClosed: true,
		},
		{
			name:       "error event",
			eventType:  EventTypeError,
			payload:    map[string]any{"message": "something went wrong"},
			wantEvent:  "error",
			wantClosed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flusher := newMockFlusher()
			writer, err := NewSSEWriter(flusher)
			if err != nil {
				t.Fatalf("NewSSEWriter() error = %v", err)
			}

			// Write the event
			err = writer.WriteEvent(tt.eventType, tt.payload)
			if err != nil {
				t.Fatalf("WriteEvent() error = %v", err)
			}

			// Get the output
			output := flusher.Body.String()

			// Verify the format is exactly: "event: <type>\ndata: <json>\n\n"
			expectedPrefix := "event: " + tt.wantEvent + "\n"
			if !strings.HasPrefix(output, expectedPrefix) {
				t.Errorf("expected output to start with %q, got %q", expectedPrefix, output)
			}

			// Verify "data: " prefix
			dataLine := "data: "
			if !strings.Contains(output, dataLine) {
				t.Errorf("expected output to contain %q, got %q", dataLine, output)
			}

			// Verify ends with double newline
			if !strings.HasSuffix(output, "\n\n") {
				t.Errorf("expected output to end with \\n\\n, got %q", output)
			}

			// Verify the exact format by parsing
			lines := strings.Split(output, "\n")
			if len(lines) < 3 {
				t.Fatalf("expected at least 3 lines (event, data, empty), got %d", len(lines))
			}

			// Check first line is event line
			if lines[0] != "event: "+tt.wantEvent {
				t.Errorf("first line should be %q, got %q", "event: "+tt.wantEvent, lines[0])
			}

			// Check second line starts with data:
			if !strings.HasPrefix(lines[1], "data: ") {
				t.Errorf("second line should start with 'data: ', got %q", lines[1])
			}

			// Extract and verify JSON payload
			jsonData := strings.TrimPrefix(lines[1], "data: ")
			var parsedPayload map[string]any
			if err := json.Unmarshal([]byte(jsonData), &parsedPayload); err != nil {
				t.Errorf("failed to parse JSON payload: %v", err)
			}

			// Verify flusher was called
			if !flusher.flushed {
				t.Error("expected Flush() to be called")
			}

			// Verify stream closed state for terminal events
			if tt.wantClosed {
				// After done or error, subsequent writes should fail
				err = writer.WriteEvent(EventTypeTextDelta, map[string]any{"delta": "test"})
				if err != ErrStreamClosed {
					t.Errorf("expected ErrStreamClosed after terminal event, got %v", err)
				}
			}
		})
	}
}

// TestSSEEventFormat_ByteExact tests the exact byte format of SSE events.
func TestSSEEventFormat_ByteExact(t *testing.T) {
	flusher := newMockFlusher()
	writer, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	payload := map[string]any{"message": "test"}
	err = writer.WriteEvent(EventTypeTextDelta, payload)
	if err != nil {
		t.Fatalf("WriteEvent() error = %v", err)
	}

	output := flusher.Body.Bytes()

	// Verify exact format: "event: text_delta\ndata: {\"message\":\"test\"}\n\n"
	expectedEvent := "event: text_delta\n"
	if !bytes.HasPrefix(output, []byte(expectedEvent)) {
		t.Errorf("expected output to start with %q, got %q", expectedEvent, output)
	}

	// Verify overall ends with \n\n
	if !bytes.HasSuffix(output, []byte("\n\n")) {
		t.Errorf("expected output to end with \\n\\n, got %q", output)
	}

	// Verify the structure by splitting
	outputStr := string(output)
	// The format should be exactly: "event: <type>\ndata: <json>\n\n"
	if !strings.HasPrefix(outputStr, "event: text_delta\ndata: ") {
		t.Errorf("expected format 'event: text_delta\\ndata: <json>\\n\\n', got %q", outputStr)
	}

	// Count the newlines - should have exactly 3 newlines (one after event, one after data, one terminal)
	// Format: "event: type\n" + "data: json\n" + "\n"
	// That's: event line with newline, data line with newline, empty line with newline
	// Actually SSE format is "event: type\ndata: json\n\n" which has 3 newlines total
	newlineCount := bytes.Count(output, []byte("\n"))
	if newlineCount != 3 {
		t.Errorf("expected exactly 3 newlines in output, got %d: %q", newlineCount, outputStr)
	}

	// Parse and verify JSON is valid
	lines := strings.Split(outputStr, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (event, data, empty), got %d: %q", len(lines), outputStr)
	}

	// First line should be the event line
	if lines[0] != "event: text_delta" {
		t.Errorf("expected first line %q, got %q", "event: text_delta", lines[0])
	}

	// Second line should start with data:
	if !strings.HasPrefix(lines[1], "data: ") {
		t.Errorf("expected second line to start with 'data: ', got %q", lines[1])
	}

	// Extract and verify JSON
	jsonData := strings.TrimPrefix(lines[1], "data: ")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonData), &parsed); err != nil {
		t.Errorf("failed to parse JSON payload: %v", err)
	}
	if parsed["message"] != "test" {
		t.Errorf("expected message 'test', got %v", parsed["message"])
	}
}

// TestSSEDoneTerminates tests that writing after done is rejected.
func TestSSEDoneTerminates(t *testing.T) {
	flusher := newMockFlusher()
	writer, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	// Write done event
	err = writer.WriteEvent(EventTypeDone, map[string]any{})
	if err != nil {
		t.Fatalf("WriteEvent(done) error = %v", err)
	}

	// Verify subsequent writes are rejected
	err = writer.WriteEvent(EventTypeTextDelta, map[string]any{"delta": "test"})
	if err != ErrStreamClosed {
		t.Errorf("expected ErrStreamClosed after done, got %v", err)
	}

	// Verify another write still fails
	err = writer.WriteEvent(EventTypeToolCall, map[string]any{"name": "test"})
	if err != ErrStreamClosed {
		t.Errorf("expected ErrStreamClosed after done (second write), got %v", err)
	}

	// Verify Close is safe to call multiple times
	err = writer.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	err = writer.Close()
	if err != nil {
		t.Errorf("second Close() error = %v", err)
	}
}

// TestSSEErrorCloses tests that error event closes the stream.
func TestSSEErrorCloses(t *testing.T) {
	flusher := newMockFlusher()
	writer, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	// Write error event
	err = writer.WriteEvent(EventTypeError, map[string]any{"message": "test error"})
	if err != nil {
		t.Fatalf("WriteEvent(error) error = %v", err)
	}

	// Verify subsequent writes are rejected
	err = writer.WriteEvent(EventTypeTextDelta, map[string]any{"delta": "test"})
	if err != ErrStreamClosed {
		t.Errorf("expected ErrStreamClosed after error event, got %v", err)
	}

	// Verify another write still fails
	err = writer.WriteEvent(EventTypeDone, map[string]any{})
	if err != ErrStreamClosed {
		t.Errorf("expected ErrStreamClosed after error event (second write), got %v", err)
	}
}

// TestNewSSEWriter_FlusherRequired tests that NewSSEWriter returns error if Flusher not supported.
func TestNewSSEWriter_FlusherRequired(t *testing.T) {
	nonFlusher := newMockNonFlusher()
	_, err := NewSSEWriter(nonFlusher)
	if err != ErrFlusherNotSupported {
		t.Errorf("expected ErrFlusherNotSupported, got %v", err)
	}
}

// TestSSEWriter_Headers tests that correct headers are set.
func TestSSEWriter_Headers(t *testing.T) {
	flusher := newMockFlusher()
	_, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	// Check headers
	headers := flusher.Header()
	if ct := headers.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type 'text/event-stream', got %q", ct)
	}
	if cc := headers.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control 'no-cache', got %q", cc)
	}
	if conn := headers.Get("Connection"); conn != "keep-alive" {
		t.Errorf("expected Connection 'keep-alive', got %q", conn)
	}
}

func TestSSEWriter_WriteCommentAndErrors(t *testing.T) {
	t.Run("comment writes and flushes", func(t *testing.T) {
		flusher := newMockFlusher()
		writer, err := NewSSEWriter(flusher)
		if err != nil {
			t.Fatalf("NewSSEWriter() error = %v", err)
		}

		if err := writer.WriteComment("keepalive"); err != nil {
			t.Fatalf("WriteComment() error = %v", err)
		}
		if got := flusher.Body.String(); got != ": keepalive\n\n" {
			t.Fatalf("comment body = %q; want SSE comment frame", got)
		}
		if !flusher.flushed {
			t.Fatal("expected Flush() after comment")
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := writer.WriteComment("closed"); err != ErrStreamClosed {
			t.Fatalf("WriteComment after close = %v; want ErrStreamClosed", err)
		}
	})

	t.Run("event marshal error", func(t *testing.T) {
		flusher := newMockFlusher()
		writer, err := NewSSEWriter(flusher)
		if err != nil {
			t.Fatalf("NewSSEWriter() error = %v", err)
		}

		err = writer.WriteEvent(EventTypeEvidence, map[string]any{"bad": func() {}})
		if err == nil || !strings.Contains(err.Error(), "failed to marshal payload") {
			t.Fatalf("WriteEvent marshal err = %v; want marshal context", err)
		}
	})

	t.Run("event write error", func(t *testing.T) {
		writer, err := NewSSEWriter(&errorFlusher{})
		if err != nil {
			t.Fatalf("NewSSEWriter() error = %v", err)
		}

		err = writer.WriteEvent(EventTypeTextDelta, map[string]any{"delta": "test"})
		if err == nil || !strings.Contains(err.Error(), "failed to write event") {
			t.Fatalf("WriteEvent write err = %v; want write context", err)
		}
	})

	t.Run("comment write error", func(t *testing.T) {
		writer, err := NewSSEWriter(&errorFlusher{})
		if err != nil {
			t.Fatalf("NewSSEWriter() error = %v", err)
		}

		err = writer.WriteComment("keepalive")
		if err == nil || !strings.Contains(err.Error(), "failed to write comment") {
			t.Fatalf("WriteComment write err = %v; want write context", err)
		}
	})

	if got := sanitizeToolCallPayload(struct{ Name string }{Name: "plain"}); got == nil {
		t.Fatal("non-map tool payload should be returned as-is")
	}
}

// TestSSEWriter_EvidencePayloadBounded tests that evidence payloads are bounded to 10KB.
func TestSSEWriter_EvidencePayloadBounded(t *testing.T) {
	flusher := newMockFlusher()
	writer, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	// Create a payload larger than 10KB
	largePayload := map[string]any{
		"content": strings.Repeat("x", MaxEvidencePayloadSize+1),
	}

	// This should fail
	err = writer.WriteEvent(EventTypeEvidence, largePayload)
	if err != ErrEvidenceTooLarge {
		t.Errorf("expected ErrEvidenceTooLarge, got %v", err)
	}

	// Small payload should succeed
	smallPayload := map[string]any{
		"content": "small content",
	}
	err = writer.WriteEvent(EventTypeEvidence, smallPayload)
	if err != nil {
		t.Errorf("small payload should succeed, got error: %v", err)
	}

	// Payload exactly at limit should succeed
	exactPayload := map[string]any{
		"content": strings.Repeat("x", MaxEvidencePayloadSize-14), // Account for JSON overhead
	}
	data, _ := json.Marshal(exactPayload)
	if len(data) > MaxEvidencePayloadSize {
		// Adjust if needed
		exactPayload = map[string]any{
			"content": strings.Repeat("x", MaxEvidencePayloadSize-100),
		}
	}
	err = writer.WriteEvent(EventTypeEvidence, exactPayload)
	if err != nil {
		t.Errorf("payload at limit should succeed, got error: %v", err)
	}
}

// TestSSEWriter_ToolCallSanitization tests that tool_call events strip secrets.
func TestSSEWriter_ToolCallSanitization(t *testing.T) {
	flusher := newMockFlusher()
	writer, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	// Create payload with secret field
	payload := map[string]any{
		"name":     "test_tool",
		"api_key":  "secret-key-123",
		"password": "secret-password",
		"args": map[string]any{
			"query":  "hello",
			"token":  "secret-token",
			"nested": map[string]any{"credential": "nested-secret"},
		},
	}

	err = writer.WriteEvent(EventTypeToolCall, payload)
	if err != nil {
		t.Fatalf("WriteEvent() error = %v", err)
	}

	// Parse the output to verify secrets are stripped
	output := flusher.Body.String()
	// Extract JSON from data line
	lines := strings.Split(output, "\n")
	dataLine := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
			break
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(dataLine), &parsed); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Verify secret fields are removed
	if _, exists := parsed["api_key"]; exists {
		t.Error("api_key should be stripped from tool_call payload")
	}
	if _, exists := parsed["password"]; exists {
		t.Error("password should be stripped from tool_call payload")
	}

	// Verify non-secret fields are preserved
	if name, ok := parsed["name"].(string); !ok || name != "test_tool" {
		t.Error("name field should be preserved")
	}

	// Verify nested secrets are also stripped
	if args, ok := parsed["args"].(map[string]any); ok {
		if _, exists := args["token"]; exists {
			t.Error("nested token should be stripped from tool_call payload")
		}
		if nested, ok := args["nested"].(map[string]any); ok {
			if _, exists := nested["credential"]; exists {
				t.Error("deeply nested credential should be stripped from tool_call payload")
			}
		}
	}
}

// TestSSEWriter_SequentialUse tests that writer is safe for sequential use.
func TestSSEWriter_SequentialUse(t *testing.T) {
	flusher := newMockFlusher()
	writer, err := NewSSEWriter(flusher)
	if err != nil {
		t.Fatalf("NewSSEWriter() error = %v", err)
	}

	// Write multiple events sequentially
	for i := 0; i < 5; i++ {
		err = writer.WriteEvent(EventTypeTextDelta, map[string]any{"delta": "test"})
		if err != nil {
			t.Errorf("WriteEvent(%d) error = %v", i, err)
		}
	}

	// Verify all events were written
	output := flusher.Body.String()
	count := strings.Count(output, "event: text_delta")
	if count != 5 {
		t.Errorf("expected 5 events, got %d", count)
	}
}
