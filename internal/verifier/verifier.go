// Package verifier defines the contract for claim verification providers.
// Implementations submit a claim predicate to an LLM or rules engine and
// receive a structured verdict with an optional raw JSON payload.
package verifier

import (
	"context"
	"time"
)

// Request is the input to a verification call. It carries the claim text
// and the profile that owns the claim (required for isolation).
type Request struct {
	// ProfileID is the owning profile; implementations MUST scope any
	// external lookups or logging to this identifier.
	ProfileID string

	// Predicate is the natural-language claim text to be verified.
	Predicate string

	// Context provides optional supporting evidence that the verifier
	// may use when assessing the predicate.
	Context string

	// ValidFrom and ValidTo describe the claim's temporal scope when known.
	// Implementations should omit them from provider payloads when nil.
	ValidFrom *time.Time
	ValidTo   *time.Time
}

// Response is the structured result of a verification call.
type Response struct {
	// Verdict is a short label produced by the verifier (e.g. "supported",
	// "refuted", "inconclusive").
	Verdict string

	// Confidence is an optional score in [0, 1] where 1 is highest confidence.
	// Zero means the provider did not return a confidence value.
	Confidence float64

	// Reasoning is an optional human-readable explanation of the verdict.
	Reasoning string

	// RawJSON holds the raw JSON payload returned by the provider so that
	// callers can inspect or store the original response without re-parsing.
	RawJSON string
}

// Verifier is the interface that wraps a single Verify call.
// Implementations must be safe for concurrent use.
type Verifier interface {
	// Verify submits req to the underlying provider and returns a structured
	// Response. Errors are one of the sentinel types defined in errors.go:
	//   - ErrVerifierTimeout       — the provider did not respond in time
	//   - ErrVerifierProvider      — the provider returned an error status
	//   - ErrVerifierRateLimit     — the provider rate-limited the caller
	//   - ErrVerifierMalformedResponse — the provider response could not be parsed
	Verify(ctx context.Context, req Request) (Response, error)
}
