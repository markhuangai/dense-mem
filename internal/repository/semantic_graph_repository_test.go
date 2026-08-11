package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeSemanticGraphQueryDefaultsAndBounds(t *testing.T) {
	defaults := normalizeSemanticGraphQuery(SemanticGraphQuery{})
	assert.Equal(t, defaultSemanticGraphDepth, defaults.Depth)
	assert.Equal(t, defaultSemanticGraphLimit, defaults.Limit)

	explicit := normalizeSemanticGraphQuery(SemanticGraphQuery{Depth: 99, Limit: 181})
	assert.Equal(t, maxSemanticGraphDepth, explicit.Depth)
	assert.Equal(t, 181, explicit.Limit)

	large := normalizeSemanticGraphQuery(SemanticGraphQuery{Limit: 1_000_000})
	assert.Equal(t, 1_000_000, large.Limit)
}
