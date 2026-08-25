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
	Model      string
	Messages   []Message
	SchemaName string
	Schema     map[string]any
	// Temperature is an optional transport hint; a capability adapter may
	// apply or ignore it according to its provider contract.
	Temperature *float64
	// MaxInputTokens is a semantic budget enforced by the capability adapter;
	// a shared transport may not enforce it.
	MaxInputTokens int
	// MaxOutputTokens is a semantic budget enforced by the capability adapter;
	// a shared transport may not enforce it.
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
