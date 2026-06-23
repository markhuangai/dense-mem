package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

func TestRecallFeedbackGraphResolverResolvesRetractedFragmentsByID(t *testing.T) {
	reader := &recallFeedbackScopedReaderStub{
		rows: []map[string]any{{
			"id":      "fragment-1",
			"type":    "fragment",
			"status":  "retracted",
			"content": "retracted content",
		}},
	}
	resolver := NewRecallFeedbackGraphResolver(reader)

	got, err := resolver.ResolveRecallFeedbackResults(context.Background(), "profile-1", []domain.RecallFeedbackResultRef{
		{Type: domain.RecallFeedbackResultTypeFragment, ID: "fragment-1", Rank: 1},
		{Type: domain.RecallFeedbackResultTypeFragment, ID: "missing", Rank: 2},
	})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "retracted", got[0].CurrentStatus)
	assert.Equal(t, "found", got[0].ResolutionStatus)
	assert.Equal(t, "missing", got[1].ResolutionStatus)
	assert.Contains(t, reader.lastQuery, "MATCH (sf:SourceFragment")
	assert.NotContains(t, reader.lastQuery, neo4jstorage.FragmentActiveFilter)
}

func TestRecallFeedbackGraphResolverGroupsKnownResultTypes(t *testing.T) {
	reader := &recallFeedbackScopedReaderStub{}
	resolver := NewRecallFeedbackGraphResolver(reader)

	_, err := resolver.ResolveRecallFeedbackResults(context.Background(), "profile-1", []domain.RecallFeedbackResultRef{
		{Type: domain.RecallFeedbackResultTypeFact, ID: "fact-1", Rank: 1},
		{Type: domain.RecallFeedbackResultTypeClaim, ID: "claim-1", Rank: 2},
	})

	require.NoError(t, err)
	assert.Equal(t, 2, reader.calls)
	assert.True(t, strings.Contains(reader.queries[0], ":Fact") || strings.Contains(reader.queries[1], ":Fact"))
	assert.True(t, strings.Contains(reader.queries[0], ":Claim") || strings.Contains(reader.queries[1], ":Claim"))
}

type recallFeedbackScopedReaderStub struct {
	lastQuery string
	queries   []string
	calls     int
	rows      []map[string]any
	err       error
}

func (s *recallFeedbackScopedReaderStub) ScopedRead(_ context.Context, _ string, query string, _ map[string]any) (any, []map[string]any, error) {
	s.calls++
	s.lastQuery = query
	s.queries = append(s.queries, query)
	return nil, s.rows, s.err
}
