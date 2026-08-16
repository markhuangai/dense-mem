package redis

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyBuilder_RateLimit_And_Stream_AreTheOnlyLiveMethods(t *testing.T) {
	kb := NewKeyBuilder()

	rateKey, err := kb.RateLimit("profile123", "route:/api/search")
	require.NoError(t, err)
	assert.Equal(t, "profile:profile123:ratelimit:route:/api/search", rateKey)

	streamKey, err := kb.Stream("profile123", "count")
	require.NoError(t, err)
	assert.Equal(t, "profile:profile123:stream:count", streamKey)
}

func TestKeyBuilder_ValidCategories_PruneToRateLimitAndStream(t *testing.T) {
	assert.Len(t, validCategories, 2)
	assert.True(t, validCategories["ratelimit"])
	assert.True(t, validCategories["stream"])
	assert.False(t, validCategories["cache"])
	assert.False(t, validCategories["session"])
	assert.Equal(t, "category must be one of: ratelimit, stream", ErrInvalidCategory.Error())
}

func TestKeyBuilder_ValidationStillWorksAfterPrune(t *testing.T) {
	kb := NewKeyBuilder()

	_, err := kb.RateLimit("", "abc")
	assert.ErrorIs(t, err, ErrEmptyNamespaceID)

	_, err = kb.Stream("profile123", "")
	assert.ErrorIs(t, err, ErrEmptyIdentifier)

	_, err = kb.buildKey("profile123", "cache", "abc")
	assert.ErrorIs(t, err, ErrInvalidCategory)
}
