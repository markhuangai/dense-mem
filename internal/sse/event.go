package sse

import "encoding/json"

// Event type constants for SSE events.
const (
	EventTypeToolCall  = "tool_call"
	EventTypeTextDelta = "text_delta"
	EventTypeEvidence  = "evidence"
	EventTypeDone      = "done"
	EventTypeError     = "error"
)

// MaxEvidencePayloadSize is the maximum size for evidence payloads (10KB).
const MaxEvidencePayloadSize = 10 * 1024

// SSEEvent represents a single Server-Sent Event.
type SSEEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// SSEEventModel is the companion interface for SSEEvent.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type SSEEventModel interface {
	GetType() string
	GetData() json.RawMessage
}

// Ensure SSEEvent implements SSEEventModel.
var _ SSEEventModel = (*SSEEvent)(nil)

// GetType returns the event type.
func (e *SSEEvent) GetType() string {
	return e.Type
}

// GetData returns the event data.
func (e *SSEEvent) GetData() json.RawMessage {
	return e.Data
}
