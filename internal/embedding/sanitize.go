package embedding

import (
	"regexp"
	"strings"
)

// redactedPlaceholder is used to replace sensitive values in error messages.
const redactedPlaceholder = "[REDACTED]"

// sanitizedError is a typed error wrapper that exposes a scrubbed public
// message via Error() while preserving the original error chain via Unwrap()
// so errors.Is/As against sentinels like ErrEmbeddingTimeout keeps working.
type sanitizedError struct {
	msg  string
	orig error
}

func (e *sanitizedError) Error() string { return e.msg }

// Unwrap returns the original error so sentinel comparisons survive
// sanitization. Callers that inspect the chain (errors.Is/As) see the real
// error; callers that call Error() see the scrubbed message.
func (e *sanitizedError) Unwrap() error { return e.orig }

// SanitizeError wraps an error with a stable public message and scrubs any
// sensitive data (API keys, bearer tokens) from the error output.
// The original error's message is never exposed via Error(), but the
// underlying error is reachable via errors.Unwrap / errors.Is / errors.As
// so sentinel-based branching (e.g. ErrEmbeddingTimeout) still functions.
func SanitizeError(err error, apiKey string) error {
	if err == nil {
		return nil
	}

	scrubbedMsg := "embedding provider error: " + scrubMessage(err.Error(), apiKey)
	return &sanitizedError{msg: scrubbedMsg, orig: err}
}

// scrubMessage removes sensitive data from an error message.
// It replaces API keys and bearer tokens with [REDACTED].
func scrubMessage(msg string, apiKey string) string {
	result := msg

	// Scrub the API key if provided
	if apiKey != "" {
		result = strings.ReplaceAll(result, apiKey, redactedPlaceholder)
	}

	// Scrub bearer tokens (case-insensitive pattern)
	// Matches "Bearer " followed by any characters until whitespace or end
	bearerPattern := regexp.MustCompile(`(?i)Bearer\s+[^\s]+`)
	result = bearerPattern.ReplaceAllString(result, "Bearer "+redactedPlaceholder)

	// Also scrub any sk- prefixed strings (common API key format)
	skPattern := regexp.MustCompile(`sk-[a-zA-Z0-9]+`)
	result = skPattern.ReplaceAllString(result, redactedPlaceholder)

	return result
}

// ScrubLogFields scrubs sensitive values from structured log fields.
// It processes key-value pairs and redacts API keys and bearer tokens.
func ScrubLogFields(fields ...any) []any {
	if len(fields) == 0 {
		return fields
	}

	result := make([]any, len(fields))
	for i, field := range fields {
		switch v := field.(type) {
		case string:
			result[i] = scrubMessage(v, "")
		default:
			result[i] = field
		}
	}

	return result
}
