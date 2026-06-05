package embedding

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeError_NilError(t *testing.T) {
	result := SanitizeError(nil, "sk-test")
	assert.Nil(t, result)
}

func TestSanitizeError_ScrubsAPIKey(t *testing.T) {
	raw := errors.New("auth failure: Authorization: Bearer sk-abc123")
	out := SanitizeError(raw, "sk-abc123")
	assert.NotContains(t, out.Error(), "sk-abc123")
	assert.Contains(t, out.Error(), "[REDACTED]")
	assert.Contains(t, out.Error(), "embedding provider error")
	assert.ErrorIs(t, out, raw)
	assert.Equal(t, raw, errors.Unwrap(out))
}

func TestSanitizeError_ScrubsBearerToken(t *testing.T) {
	raw := errors.New("request failed: Bearer sk-secretkey123 data")
	out := SanitizeError(raw, "")
	assert.NotContains(t, out.Error(), "sk-secretkey123")
	assert.Contains(t, out.Error(), "[REDACTED]")
}

func TestSanitizeError_ScrubsSKPattern(t *testing.T) {
	raw := errors.New("error in sk-abcdefghijk response")
	out := SanitizeError(raw, "")
	assert.NotContains(t, out.Error(), "sk-abcdefghijk")
	assert.Contains(t, out.Error(), "[REDACTED]")
}

func TestSanitizeError_PreservesNonSensitive(t *testing.T) {
	raw := errors.New("connection timeout after 30s")
	out := SanitizeError(raw, "")
	assert.Contains(t, out.Error(), "connection timeout")
	assert.Contains(t, out.Error(), "30s")
}

func TestSanitizeError_ScrubsMultipleKeys(t *testing.T) {
	raw := errors.New("failed with sk-aaa and sk-bbb keys")
	out := SanitizeError(raw, "sk-aaa")
	assert.NotContains(t, out.Error(), "sk-aaa")
	assert.NotContains(t, out.Error(), "sk-bbb")
}

func TestScrubLogFields_NilInput(t *testing.T) {
	result := ScrubLogFields()
	assert.Empty(t, result)
}

func TestScrubLogFields_StringWithAPIKey(t *testing.T) {
	fields := []any{"error", "failed with sk-secret123", "count", 5}
	result := ScrubLogFields(fields...)
	assert.NotContains(t, result[1], "sk-secret123")
	assert.Contains(t, result[1], "[REDACTED]")
	assert.Equal(t, 5, result[3])
}

func TestScrubLogFields_NonStringValues(t *testing.T) {
	fields := []any{"count", 42, "enabled", true, "rate", 3.14}
	result := ScrubLogFields(fields...)
	assert.Equal(t, 42, result[1])
	assert.Equal(t, true, result[3])
	assert.Equal(t, 3.14, result[5])
}
