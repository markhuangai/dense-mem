package modelprovider

import "context"

// Message is one bounded structured-output conversation message.
type Message struct {
	Role    string
	Content string
}

// StructuredRequest describes a provider-owned JSON-schema request without
// exposing application policy or durable identifiers.
type StructuredRequest struct {
	Model           string
	Messages        []Message
	SchemaName      string
	Schema          map[string]any
	Temperature     *float64
	MaxInputTokens  int
	MaxOutputTokens int
}

// StructuredResult contains provider content and bounded usage metadata.
type StructuredResult struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StructuredTransport is the shared transport seam for model-backed
// structured-output providers. Capability-specific adapters own validation.
type StructuredTransport interface {
	Complete(context.Context, StructuredRequest) (StructuredResult, error)
}
