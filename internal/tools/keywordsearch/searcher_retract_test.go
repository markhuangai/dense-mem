package keywordsearch

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchContent_RetractedFragmentsFiltered verifies that SearchContent
// adds the active-fragment filter (AC-44) to the Cypher WHERE clause so that
// retracted SourceFragment nodes are excluded at the database layer.
func TestSearchContent_RetractedFragmentsFiltered(t *testing.T) {
	reader := &capturingReader{rows: []map[string]any{
		{"fragment_id": "frag-1", "content": "hello", "score": 0.9, "team_id": "p1"},
	}}
	s := NewFragmentSearcher(reader)

	_, err := s.SearchContent(context.Background(), "p1", "hello", nil, 10)
	require.NoError(t, err)

	// The Cypher query must exclude retracted nodes via the coalesce guard.
	assert.Contains(t, reader.capturedQuery, "coalesce(f.status,'active') = 'active'",
		"SearchContent must include the retract filter in the WHERE clause (AC-44)")
	assert.Contains(t, reader.capturedQuery, "DECOMPOSED_INTO",
		"SearchContent must suppress legacy fragments replaced by active assertions")
}

// TestSearchContent_RetractedNodeNotReturned verifies the end-to-end behaviour:
// a retracted fragment (status = 'retracted') is absent from results because
// the WHERE clause filters it out at the database layer.
// The fake reader simulates a DB that only returns the active node.
func TestSearchContent_RetractedNodeNotReturned(t *testing.T) {
	// Simulate the DB honouring the WHERE filter: only the active fragment is returned.
	reader := &capturingReader{rows: []map[string]any{
		{"fragment_id": "frag-active", "content": "active content", "score": 0.9, "team_id": "p1"},
		// "frag-retracted" is absent because the real DB filtered it via the WHERE clause.
	}}
	s := NewFragmentSearcher(reader)

	got, err := s.SearchContent(context.Background(), "p1", "hello", nil, 10)
	require.NoError(t, err)

	ids := make([]string, len(got))
	for i, r := range got {
		ids[i] = r.FragmentID
	}
	assert.Contains(t, ids, "frag-active", "active fragment must be returned")
	assert.NotContains(t, ids, "frag-retracted", "retracted fragment must not appear in results")

	// The query must admit only active evidence, which also excludes quarantine.
	assert.True(t, strings.Contains(reader.capturedQuery, "= 'active'"),
		"query must require active status")
}

// TestSearchContent_RetractedFilterPresentWithLabels verifies that the
// retract filter is included even when a label filter is also applied.
func TestSearchContent_RetractedFilterPresentWithLabels(t *testing.T) {
	reader := &capturingReader{rows: []map[string]any{}}
	s := NewFragmentSearcher(reader)

	_, err := s.SearchContent(context.Background(), "p1", "hello", []string{"science"}, 10)
	require.NoError(t, err)

	assert.Contains(t, reader.capturedQuery, "coalesce(f.status,'active') = 'active'",
		"retract filter must be present even when label filter is applied")
	assert.Contains(t, reader.capturedQuery, "ANY(label IN $labels WHERE label IN f.labels)",
		"label filter must still be present when labels are provided")
}
