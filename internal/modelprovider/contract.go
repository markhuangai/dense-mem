package modelprovider

import "context"

// Message is one bounded structured-output conversation message.
type Message struct {
	Role    string
	Content string
}

const StructuredResponseLimit = 3

// StructuredRequest describes a provider-owned JSON-schema request without
// exposing application policy or durable identifiers.
type StructuredRequest struct {
	Model      string
	Messages   []Message
	SchemaName string
	Schema     map[string]any
	// A nil temperature omits the field from the provider envelope.
	Temperature *float64
	// The generation coordinator enforces these limits outside the HTTP envelope.
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
	UsageAvailable   bool
	StatusCode       int
	Diagnostics      *CallDiagnostics
}

// StructuredTransport is the shared transport seam for model-backed
// structured-output providers. Capability-specific adapters own validation.
type StructuredTransport interface {
	Complete(context.Context, StructuredRequest) (StructuredResult, error)
}
