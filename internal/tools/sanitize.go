package tools

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

// redactedPlaceholder replaces sensitive values in error messages exposed at
// HTTP or MCP boundaries.
const redactedPlaceholder = "[REDACTED]"

const maxPublicToolErrorRunes = 512

// bearerPattern matches "Bearer <token>" (case-insensitive).
var bearerPattern = regexp.MustCompile(`(?i)Bearer\s+\S+`)

// skPattern matches sk-... API key literals (OpenAI-style and similar).
var skPattern = regexp.MustCompile(`sk-[a-zA-Z0-9]+`)

// apiKeyPattern matches generic API key query/header literals, e.g.
// "api_key=<value>" or "apikey=<value>".
var apiKeyPattern = regexp.MustCompile(`(?i)api[_-]?key\s*=\s*\S+`)

// SanitizeError returns a bounded, scrubbed string representation of err
// suitable for exposure at HTTP response bodies and MCP tool outputs. Typed
// consumer errors retain their code and message; provider, storage, and
// internal failures are reduced to a stable operator-contact message. It removes:
//   - Bearer tokens
//   - sk-... API key literals
//   - api_key=... / apikey=... literals
//
// Returns an empty string when err is nil.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return sanitizeAPIError(apiErr)
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, modelprovider.ErrVerifierTimeout) ||
		errors.Is(err, modelprovider.ErrVerifierRateLimit) ||
		errors.Is(err, modelprovider.ErrVerifierMalformedResponse) {
		return "tool execution failed; contact an operator"
	}
	message := scrubAndBound(err.Error())
	if isInternalToolError(message) {
		return "tool execution failed; contact an operator"
	}
	return message
}

func sanitizeAPIError(err *httperr.APIError) string {
	code := string(err.Code)
	status := httperr.HTTPStatusCode(err.Code)
	if status >= 500 {
		message := "internal server error"
		switch status {
		case 502:
			message = "upstream service error"
		case 503:
			message = "service unavailable"
		case 504:
			message = "upstream service timeout"
		}
		return code + ": " + message
	}
	return scrubAndBound(code + ": " + err.Message)
}

func scrubAndBound(message string) string {
	message = scrubSensitive(strings.TrimSpace(message))
	if message == "" {
		return "unknown error"
	}
	if utf8.RuneCountInString(message) <= maxPublicToolErrorRunes {
		return message
	}
	return string([]rune(message)[:maxPublicToolErrorRunes])
}

func isInternalToolError(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"pq:", "postgres", "database", "sql:", "gorm", "redis", "dial tcp", "connection refused",
		"provider unavailable", "provider error", "provider failed", "provider response", "api key",
		"storage unavailable", "storage failed", "internal server error", "panic:", "runtime error",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// scrubSensitive applies all redaction patterns to msg.
func scrubSensitive(msg string) string {
	result := msg

	// Scrub "Bearer <token>" patterns.
	result = bearerPattern.ReplaceAllStringFunc(result, func(match string) string {
		// Preserve the "Bearer " prefix for readability.
		idx := strings.IndexFunc(match, func(r rune) bool { return r == ' ' || r == '\t' })
		if idx < 0 {
			return "Bearer " + redactedPlaceholder
		}
		return match[:idx+1] + redactedPlaceholder
	})

	// Scrub sk-... API key literals.
	result = skPattern.ReplaceAllString(result, redactedPlaceholder)

	// Scrub api_key=... / apikey=... literals.
	result = apiKeyPattern.ReplaceAllStringFunc(result, func(match string) string {
		eqIdx := strings.IndexByte(match, '=')
		if eqIdx < 0 {
			return redactedPlaceholder
		}
		return match[:eqIdx+1] + redactedPlaceholder
	})

	return result
}
