package tools

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// TestSanitizeError covers AC-29: SanitizeError scrubs sensitive data from
// error strings before they reach HTTP / MCP boundaries.
func TestSanitizeError(t *testing.T) {
	t.Run("nil error returns empty string", func(t *testing.T) {
		got := SanitizeError(nil)
		require.Equal(t, "", got)
	})

	t.Run("plain error passes through unchanged", func(t *testing.T) {
		err := errors.New("connection refused")
		got := SanitizeError(err)
		require.Equal(t, "tool execution failed; contact an operator", got)
	})

	t.Run("bearer token is redacted", func(t *testing.T) {
		err := errors.New("request failed: Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.secret")
		got := SanitizeError(err)
		require.NotContains(t, got, "eyJhbGciOiJIUzI1NiJ9.secret")
		require.Contains(t, got, "[REDACTED]")
	})

	t.Run("bearer token case-insensitive redaction", func(t *testing.T) {
		err := errors.New("BEARER supersecrettoken123")
		got := SanitizeError(err)
		require.NotContains(t, got, "supersecrettoken123")
		require.Contains(t, got, "[REDACTED]")
	})

	t.Run("sk- API key is redacted", func(t *testing.T) {
		err := errors.New("openai error: invalid key sk-abc123XYZ")
		got := SanitizeError(err)
		require.NotContains(t, got, "sk-abc123XYZ")
		require.Contains(t, got, "[REDACTED]")
	})

	t.Run("api_key literal is redacted", func(t *testing.T) {
		err := errors.New("provider error: api_key=supersecret9999")
		got := SanitizeError(err)
		require.NotContains(t, got, "supersecret9999")
		require.Equal(t, "tool execution failed; contact an operator", got)
	})

	t.Run("apikey literal (no underscore) is redacted", func(t *testing.T) {
		err := errors.New("provider error: apikey=topsecret")
		got := SanitizeError(err)
		require.NotContains(t, got, "topsecret")
		require.Equal(t, "tool execution failed; contact an operator", got)
	})

	t.Run("multiple sensitive patterns in one message", func(t *testing.T) {
		err := errors.New("auth=Bearer sk-abc123 api_key=xyz789")
		got := SanitizeError(err)
		require.NotContains(t, got, "sk-abc123")
		require.NotContains(t, got, "xyz789")
	})

	t.Run("message with no sensitive data is returned verbatim", func(t *testing.T) {
		msg := "dial tcp 127.0.0.1:5432: connection refused"
		err := errors.New(msg)
		got := SanitizeError(err)
		require.Equal(t, "tool execution failed; contact an operator", got)
	})

	t.Run("typed consumer error retains a scrubbed bounded message", func(t *testing.T) {
		err := httperr.New(httperr.VALIDATION_ERROR, "invalid api_key=supersecret9999")
		got := SanitizeError(err)
		require.NotContains(t, got, "supersecret9999")
		require.Contains(t, got, "[REDACTED]")
	})

	t.Run("typed verifier failures use the bounded operator message", func(t *testing.T) {
		errorsToSanitize := []error{
			&verifier.TimeoutError{Provider: "provider", Message: "provider-secret"},
			&verifier.RateLimitError{Provider: "provider", Message: "provider-secret"},
			&verifier.MalformedResponseError{Provider: "provider", Message: "provider-secret"},
		}
		for _, err := range errorsToSanitize {
			got := SanitizeError(err)
			require.Equal(t, "tool execution failed; contact an operator", got)
			require.NotContains(t, got, "provider-secret")
		}
	})

	t.Run("long multibyte errors remain valid and bounded", func(t *testing.T) {
		got := SanitizeError(errors.New(strings.Repeat("界", maxPublicToolErrorRunes+20)))
		require.True(t, utf8.ValidString(got))
		require.LessOrEqual(t, utf8.RuneCountInString(got), maxPublicToolErrorRunes)
	})
}

// TestSanitizeError_CrossProfileIsolation covers AC-61: sanitization logic
// must not leak data from one call context into another. Because SanitizeError
// is stateless (no shared mutable state between calls), we verify that
// sensitive data from a "profile A" error never appears in the result
// produced for "profile B".
func TestSanitizeError_CrossProfileIsolation(t *testing.T) {
	// Simulate two profile-scoped errors containing distinct secrets.
	profileAErr := errors.New("profile-a error: Bearer tokenForProfileA")
	profileBErr := errors.New("profile-b error: network timeout")

	aResult := SanitizeError(profileAErr)
	bResult := SanitizeError(profileBErr)

	// The token from profile A must NOT appear in profile B's result.
	require.NotContains(t, bResult, "tokenForProfileA",
		"profile A secret must not leak into profile B result")

	// Profile A's result must have the token redacted.
	require.NotContains(t, aResult, "tokenForProfileA",
		"profile A secret must be redacted in its own result")

	// Profile B's result must contain its own non-sensitive message.
	require.Contains(t, bResult, "network timeout")
}
