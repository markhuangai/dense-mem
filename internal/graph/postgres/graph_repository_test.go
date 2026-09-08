package postgres

import (
	"testing"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeGraphQueryDefaultsAndBounds(t *testing.T) {
	defaults := normalizeSemanticGraphQuery(graphcontract.Query{})
	assert.Equal(t, defaultSemanticGraphDepth, defaults.Depth)
	assert.Equal(t, defaultSemanticGraphLimit, defaults.Limit)

	explicit := normalizeSemanticGraphQuery(graphcontract.Query{Depth: 99, Limit: 181})
	assert.Equal(t, maxSemanticGraphDepth, explicit.Depth)
	assert.Equal(t, 181, explicit.Limit)

	large := normalizeSemanticGraphQuery(graphcontract.Query{Limit: 1_000_000})
	assert.Equal(t, 1_000_000, large.Limit)
}
