package fragmentdedupe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScopedReader is a test double for ScopedReader.
type fakeScopedReader struct {
	LastQuery  string
	LastParams map[string]any
	Results    []map[string]any
	Error      error
}

// ScopedRead implements ScopedReader for testing.
func (f *fakeScopedReader) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error) {
	f.LastQuery = query
	f.LastParams = params
	if params == nil {
		f.LastParams = make(map[string]any)
	}
	// Add profileId to params to match real behavior
	f.LastParams["profileId"] = profileID

	if f.Error != nil {
		return nil, nil, f.Error
	}
	return nil, f.Results, nil
}

// TestDedupeLookup_ProfileScoped_IdempotencyKey verifies that ByIdempotencyKey
// uses profile-scoped queries (AC-21).
func TestDedupeLookup_ProfileScoped_IdempotencyKey(t *testing.T) {
	reader := &fakeScopedReader{Results: nil}
	lookup := NewNeo4jDedupeLookup(reader)

	got, err := lookup.ByIdempotencyKey(context.Background(), "pA", "test-key")
	require.NoError(t, err)
	assert.Nil(t, got, "miss should return nil fragment")

	// Verify the query includes team_id filtering
	assert.Contains(t, reader.LastQuery, "team_id", "query must filter by team_id")
	assert.Contains(t, reader.LastQuery, "$profileId", "query must use $profileId placeholder")
	assert.Contains(t, reader.LastQuery, "idempotency_key", "query must filter by idempotency_key")
	assert.Equal(t, "pA", reader.LastParams["profileId"])
	assert.Equal(t, "test-key", reader.LastParams["key"])
}

// TestDedupeLookup_ProfileScoped_ContentHash verifies that ByContentHash
// uses profile-scoped queries (AC-22).
func TestDedupeLookup_ProfileScoped_ContentHash(t *testing.T) {
	reader := &fakeScopedReader{Results: nil}
	lookup := NewNeo4jDedupeLookup(reader)

	hash := "abc123def456"
	got, err := lookup.ByContentHash(context.Background(), "profile-xyz", hash)
	require.NoError(t, err)
	assert.Nil(t, got, "miss should return nil fragment")

	// Verify the query includes team_id filtering
	assert.Contains(t, reader.LastQuery, "team_id", "query must filter by team_id")
	assert.Contains(t, reader.LastQuery, "$profileId", "query must use $profileId placeholder")
	assert.Contains(t, reader.LastQuery, "content_hash", "query must filter by content_hash")
	assert.Equal(t, "profile-xyz", reader.LastParams["profileId"])
	assert.Equal(t, hash, reader.LastParams["hash"])
}

// TestDedupeLookup_ByIdempotencyKey_Hit verifies that ByIdempotencyKey
// returns a fragment when found.
func TestDedupeLookup_ByIdempotencyKey_Hit(t *testing.T) {
	reader := &fakeScopedReader{
		Results: []map[string]any{
			{
				"fragment_id":     "frag-123",
				"team_id":         "profile-xyz",
				"content":         "test content",
				"source":          "test-source",
				"source_type":     "document",
				"content_hash":    "hash123",
				"idempotency_key": "test-key",
			},
		},
	}
	lookup := NewNeo4jDedupeLookup(reader)

	got, err := lookup.ByIdempotencyKey(context.Background(), "profile-xyz", "test-key")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "frag-123", got.FragmentID)
	assert.Equal(t, "profile-xyz", got.ProfileID)
	assert.Equal(t, "test content", got.Content)
	assert.Equal(t, "test-key", got.IdempotencyKey)
}

// TestDedupeLookup_ByContentHash_Hit verifies that ByContentHash
// returns a fragment when found.
func TestDedupeLookup_ByContentHash_Hit(t *testing.T) {
	reader := &fakeScopedReader{
		Results: []map[string]any{
			{
				"fragment_id":  "frag-456",
				"team_id":      "profile-abc",
				"content":      "hello world",
				"content_hash": "hash456",
				"source_type":  "conversation",
			},
		},
	}
	lookup := NewNeo4jDedupeLookup(reader)

	got, err := lookup.ByContentHash(context.Background(), "profile-abc", "hash456")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "frag-456", got.FragmentID)
	assert.Equal(t, "profile-abc", got.ProfileID)
	assert.Equal(t, "hello world", got.Content)
	assert.Equal(t, "hash456", got.ContentHash)
}

// TestDedupeLookup_UsesCompositeIndex verifies that queries use the
// property patterns that leverage composite indexes (from Unit 12).
func TestDedupeLookup_UsesCompositeIndex(t *testing.T) {
	reader := &fakeScopedReader{Results: nil}
	lookup := NewNeo4jDedupeLookup(reader)

	// Test idempotency key lookup uses the pattern for composite index
	_, _ = lookup.ByIdempotencyKey(context.Background(), "p1", "k1")
	assert.Contains(t, reader.LastQuery, "MATCH (sf:SourceFragment", "query must match SourceFragment nodes")
	assert.Contains(t, reader.LastQuery, "team_id: $profileId", "query must use team_id property")
	assert.Contains(t, reader.LastQuery, "idempotency_key: $key", "query must use idempotency_key property")

	// Test content hash lookup uses the pattern for composite index
	_, _ = lookup.ByContentHash(context.Background(), "p1", "h1")
	assert.Contains(t, reader.LastQuery, "content_hash: $hash", "query must use content_hash property")
}

// TestDedupeLookup_ReturnsNilNilOnMiss verifies the miss contract:
// (nil, nil) on miss, not an error.
func TestDedupeLookup_ReturnsNilNilOnMiss(t *testing.T) {
	reader := &fakeScopedReader{Results: nil} // empty results = miss
	lookup := NewNeo4jDedupeLookup(reader)

	frag1, err1 := lookup.ByIdempotencyKey(context.Background(), "p1", "missing-key")
	assert.NoError(t, err1, "miss should not return error")
	assert.Nil(t, frag1, "miss should return nil fragment")

	frag2, err2 := lookup.ByContentHash(context.Background(), "p1", "missing-hash")
	assert.NoError(t, err2, "miss should not return error")
	assert.Nil(t, frag2, "miss should return nil fragment")
}

// TestDedupeLookup_InterfaceAssertion verifies compile-time interface implementation.
func TestDedupeLookup_InterfaceAssertion(t *testing.T) {
	// This test exists to ensure the compile-time assertion in dedupe.go is valid.
	// If the assertion fails, the code won't compile.
	reader := &fakeScopedReader{}
	var _ DedupeLookup = NewNeo4jDedupeLookup(reader)
	assert.True(t, true, "interface assertion passed")
}
