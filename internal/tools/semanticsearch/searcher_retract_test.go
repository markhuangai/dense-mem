package semanticsearch

import (
	"context"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingScopedReader records the last Cypher query passed to ScopedRead.
type capturingScopedReader struct {
	capturedQuery string
	rows          []map[string]any
}

func (c *capturingScopedReader) ScopedRead(_ context.Context, _ string, query string, _ map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	c.capturedQuery = query
	return nil, c.rows, nil
}

// TestQueryVectorIndex_RetractedFragmentsFiltered verifies that QueryVectorIndex
// includes the active-fragment filter (AC-44) in the Cypher WHERE clause so
// retracted SourceFragment nodes are excluded at the database layer.
func TestQueryVectorIndex_RetractedFragmentsFiltered(t *testing.T) {
	reader := &capturingScopedReader{rows: []map[string]any{}}
	s := NewEmbeddingSearcher(reader)

	_, err := s.QueryVectorIndex(context.Background(), "p1", []float32{0.1, 0.2, 0.3}, 10)
	require.NoError(t, err)

	assert.Contains(t, reader.capturedQuery, "coalesce(f.status,'active') = 'active'",
		"QueryVectorIndex must include the retract filter in the WHERE clause (AC-44)")
	assert.Contains(t, reader.capturedQuery, "DECOMPOSED_INTO",
		"QueryVectorIndex must suppress legacy fragments replaced by active assertions")
}

// TestQueryVectorIndex_RetractedNodeNotReturned verifies the end-to-end
// behaviour: the active-fragment filter is present so the DB does not return
// retracted nodes. The fake reader simulates a DB that honours the WHERE clause.
func TestQueryVectorIndex_RetractedNodeNotReturned(t *testing.T) {
	// Simulate the DB returning only the active fragment (retracted one filtered out).
	reader := &capturingScopedReader{rows: []map[string]any{
		{"id": "frag-active", "content": "active content", "score": float64(0.9), "labels": []any{}, "metadata": map[string]any{}, "team_id": "p1"},
	}}
	s := NewEmbeddingSearcher(reader)

	got, err := s.QueryVectorIndex(context.Background(), "p1", []float32{0.1, 0.2, 0.3}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "frag-active", got[0].ID, "active fragment must be returned")

	// The WHERE clause admits only active evidence, excluding quarantine too.
	assert.Contains(t, reader.capturedQuery, "= 'active'",
		"query must require active status")
}
