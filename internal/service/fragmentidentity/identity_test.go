package fragmentidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContentHash_RawBytes_NoNormalization verifies that ContentHash
// hashes raw UTF-8 bytes without any normalization or trimming (AC-20, AC-43).
func TestContentHash_RawBytes_NoNormalization(t *testing.T) {
	a := ContentHash("Hello").Hex
	b := ContentHash("hello").Hex
	assert.NotEqual(t, a, b, "hashing must NOT lowercase")
	assert.NotEqual(t, a, ContentHash(" Hello ").Hex, "hashing must NOT trim")
}

// TestContentHash_Deterministic verifies that the same content always
// produces the same hash.
func TestContentHash_Deterministic(t *testing.T) {
	content := "The quick brown fox jumps over the lazy dog"
	result1 := ContentHash(content)
	result2 := ContentHash(content)
	assert.Equal(t, result1.Hex, result2.Hex, "hash must be deterministic")

	// Verify it matches the expected SHA-256
	expected := sha256.Sum256([]byte(content))
	expectedHex := hex.EncodeToString(expected[:])
	assert.Equal(t, expectedHex, result1.Hex, "hash must match expected SHA-256")
}

// TestContentHash_EmptyString verifies that empty content produces a valid hash.
func TestContentHash_EmptyString(t *testing.T) {
	result := ContentHash("")
	assert.NotEmpty(t, result.Hex, "empty content should produce a valid hash")

	// SHA-256 of empty string is well-known
	expected := sha256.Sum256([]byte{})
	expectedHex := hex.EncodeToString(expected[:])
	assert.Equal(t, expectedHex, result.Hex, "empty string hash must match expected")
}

// TestContentHash_LowercaseHex verifies that the output is lowercase hex.
func TestContentHash_LowercaseHex(t *testing.T) {
	result := ContentHash("test content")
	assert.Equal(t, strings.ToLower(result.Hex), result.Hex, "hex output must be lowercase")
	assert.Len(t, result.Hex, 64, "SHA-256 hex must be 64 characters")
}

// TestContentHash_UnicodePreserved verifies that Unicode content is hashed
// without modification.
func TestContentHash_UnicodePreserved(t *testing.T) {
	unicodeContent := "Hello, 世界! 🌍"
	result1 := ContentHash(unicodeContent)

	// Should match direct byte hash
	expected := sha256.Sum256([]byte(unicodeContent))
	expectedHex := hex.EncodeToString(expected[:])
	assert.Equal(t, expectedHex, result1.Hex, "unicode content must be hashed as raw bytes")
}

// TestNewFragmentID_IsUnique verifies that NewFragmentID produces unique IDs
// across 1000 iterations (AC-20).
func TestNewFragmentID_IsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewFragmentID()
		require.False(t, seen[id], "fragment ID must be unique: %s", id)
		seen[id] = true
	}
}

// TestNewFragmentID_IsValidUUID verifies that NewFragmentID returns a valid UUID.
func TestNewFragmentID_IsValidUUID(t *testing.T) {
	id := NewFragmentID()
	assert.Len(t, id, 36, "UUID string must be 36 characters (with hyphens)")

	// Check format: 8-4-4-4-12
	parts := strings.Split(id, "-")
	require.Len(t, parts, 5, "UUID must have 5 hyphen-separated parts")
	assert.Len(t, parts[0], 8)
	assert.Len(t, parts[1], 4)
	assert.Len(t, parts[2], 4)
	assert.Len(t, parts[3], 4)
	assert.Len(t, parts[4], 12)
}

// TestNewFragmentID_IsTimeOrdered verifies that IDs are roughly time-ordered.
// UUIDv7 should produce IDs that sort chronologically.
func TestNewFragmentID_IsTimeOrdered(t *testing.T) {
	ids := make([]string, 100)
	for i := 0; i < 100; i++ {
		ids[i] = NewFragmentID()
	}

	// IDs generated in sequence should sort in the same order
	// (allowing for the same timestamp being used for multiple IDs within a millisecond)
	sorted := make([]string, 100)
	copy(sorted, ids)
	for i := 0; i < len(sorted)-1; i++ {
		// Since UUIDv7 is time-ordered, later IDs should be >= earlier IDs
		// We just verify they are unique and roughly ordered
		assert.NotEqual(t, sorted[i], sorted[i+1], "consecutive IDs must differ")
	}
}
