package graphview

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSemanticQueryDefaultsAndBounds(t *testing.T) {
	defaults, err := normalizeSemanticQuery(Query{})
	require.NoError(t, err)
	assert.Equal(t, DefaultDepth, defaults.depth)
	assert.Equal(t, DefaultLimit, defaults.limit)

	explicit, err := normalizeSemanticQuery(Query{Depth: 99, Limit: 181})
	require.NoError(t, err)
	assert.Equal(t, MaxDepth, explicit.depth)
	assert.Equal(t, 181, explicit.limit)

	large, err := normalizeSemanticQuery(Query{Limit: 1_000_000})
	require.NoError(t, err)
	assert.Equal(t, 1_000_000, large.limit)

	local, err := normalizeSemanticQuery(Query{
		Scope: " LOCAL ", Query: " Project ", Types: []string{"entities", "VALUE", "entity", "unknown"},
		AnchorType: "values", AnchorID: " value-1 ", Depth: 1, Limit: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, ScopeLocal, local.scope)
	assert.Equal(t, "project", local.search)
	assert.Equal(t, []string{"entity", "value"}, local.types)
	assert.Equal(t, "value", local.anchorType)
	assert.Equal(t, "value-1", local.anchorID)
	assert.Equal(t, 1, local.depth)
	assert.Equal(t, 7, local.limit)

	_, err = normalizeSemanticQuery(Query{Scope: ScopeLocal})
	assert.ErrorIs(t, err, ErrMissingAnchor)
	_, err = normalizeSemanticQuery(Query{Scope: ScopeLocal, AnchorType: "evidence", AnchorID: "id"})
	assert.ErrorIs(t, err, ErrInvalidAnchorType)
}

func TestSemanticServiceGraphNormalizesAndMapsSnapshot(t *testing.T) {
	recordedAt := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	store := &semanticStoreStub{snapshot: &repository.SemanticGraphSnapshot{
		Scope: ScopeLocal, Query: "project", Depth: MaxDepth, Limit: 181, Truncated: true,
		Anchor: &repository.SemanticGraphAnchor{Type: "entity", ID: "entity-1", Key: "entity:entity-1"},
		Nodes: []repository.SemanticGraphNode{
			{Key: "entity:entity-1", ID: "entity-1", Type: "entity", Title: strings.Repeat("t", 170), Body: strings.Repeat("b", 430), Status: "active", RecordedAt: &recordedAt},
			{Key: "value:value-1", ID: "value-1", Type: "value"},
		},
		Edges: []repository.SemanticGraphEdge{{ID: "relationship-1", Source: "entity:entity-1", Target: "value:value-1", Relationship: "USES", Directed: true}},
	}}
	service := NewSemantic(store)

	snapshot, err := service.Graph(context.Background(), " team-1 ", Query{
		Scope: " LOCAL ", Query: " Project ", Types: []string{"entities", "value", "entity"},
		AnchorType: "entities", AnchorID: " entity-1 ", Depth: 99, Limit: 181,
	})
	require.NoError(t, err)
	assert.Equal(t, repository.SemanticGraphQuery{
		TeamID: "team-1", Scope: ScopeLocal, Query: "project", Types: []string{"entity", "value"},
		AnchorType: "entity", AnchorID: "entity-1", Depth: MaxDepth, Limit: 181,
	}, store.graphInput)
	assert.Equal(t, ScopeLocal, snapshot.Scope)
	assert.Equal(t, "project", snapshot.Query)
	assert.Equal(t, MaxDepth, snapshot.Depth)
	assert.Equal(t, 181, snapshot.Limit)
	assert.True(t, snapshot.Truncated)
	assert.Equal(t, &Anchor{Type: "entity", ID: "entity-1", Key: "entity:entity-1"}, snapshot.Anchor)
	require.Len(t, snapshot.Nodes, 2)
	assert.Equal(t, strings.Repeat("t", 160)+"...", snapshot.Nodes[0].Title)
	assert.Equal(t, strings.Repeat("b", maxNodeBodyRunes)+"...", snapshot.Nodes[0].Body)
	assert.Equal(t, "value-1", snapshot.Nodes[1].Title)
	assert.Equal(t, recordedAt, *snapshot.Nodes[0].RecordedAt)
	assert.Equal(t, []Edge{{ID: "relationship-1", Source: "entity:entity-1", Target: "value:value-1", Relationship: "USES", Directed: true}}, snapshot.Edges)
}

func TestSemanticServiceGraphErrorsAndEmptySnapshot(t *testing.T) {
	var nilService *semanticService
	_, err := nilService.Graph(context.Background(), "team", Query{})
	assert.ErrorContains(t, err, "not configured")

	storeErr := errors.New("store unavailable")
	store := &semanticStoreStub{graphErr: storeErr}
	_, err = NewSemantic(store).Graph(context.Background(), "team", Query{})
	assert.ErrorIs(t, err, storeErr)
	assert.ErrorContains(t, err, "semantic graph view")

	store.graphErr = nil
	store.snapshot = nil
	snapshot, err := NewSemantic(store).Graph(context.Background(), "team", Query{})
	require.NoError(t, err)
	assert.Empty(t, snapshot.Nodes)
	assert.Empty(t, snapshot.Edges)
}

func TestSemanticServiceNodeDetail(t *testing.T) {
	recordedAt := time.Date(2026, time.August, 10, 13, 0, 0, 0, time.UTC)
	store := &semanticStoreStub{detail: &repository.SemanticGraphNode{
		Key: "entity:entity-1", ID: "entity-1", Type: "entity", Title: "Dense-Mem", Body: "project", Status: "active", RecordedAt: &recordedAt,
	}}
	service := NewSemantic(store)

	detail, err := service.NodeDetail(context.Background(), " team-1 ", " Entities ", " entity-1 ")
	require.NoError(t, err)
	assert.Equal(t, repository.SemanticGraphNodeDetailInput{TeamID: "team-1", NodeType: "entity", NodeID: "entity-1"}, store.detailInput)
	assert.Equal(t, "Dense-Mem", detail.Title)
	assert.Equal(t, "project", detail.Body)
	assert.Equal(t, recordedAt, *detail.RecordedAt)

	store.detail = nil
	_, err = service.NodeDetail(context.Background(), "team", "value", "value-1")
	assert.ErrorIs(t, err, ErrNodeNotFound)

	store.detailErr = sql.ErrNoRows
	_, err = service.NodeDetail(context.Background(), "team", "value", "value-1")
	assert.ErrorIs(t, err, ErrNodeNotFound)

	storeErr := errors.New("detail unavailable")
	store.detailErr = storeErr
	_, err = service.NodeDetail(context.Background(), "team", "value", "value-1")
	assert.ErrorIs(t, err, storeErr)
	assert.ErrorContains(t, err, "semantic graph node detail")
}

func TestSemanticServiceNodeDetailValidation(t *testing.T) {
	var nilService *semanticService
	_, err := nilService.NodeDetail(context.Background(), "team", "entity", "id")
	assert.ErrorContains(t, err, "not configured")

	service := NewSemantic(&semanticStoreStub{})
	_, err = service.NodeDetail(context.Background(), "team", "evidence", "id")
	assert.ErrorIs(t, err, ErrInvalidNodeType)
	_, err = service.NodeDetail(context.Background(), "team", "entity", "")
	assert.ErrorIs(t, err, ErrMissingNode)
	_, err = service.NodeDetail(context.Background(), "team", "", "id")
	assert.ErrorIs(t, err, ErrMissingNode)
}

type semanticStoreStub struct {
	graphInput  repository.SemanticGraphQuery
	snapshot    *repository.SemanticGraphSnapshot
	graphErr    error
	detailInput repository.SemanticGraphNodeDetailInput
	detail      *repository.SemanticGraphNode
	detailErr   error
}

func (s *semanticStoreStub) SemanticGraph(_ context.Context, input repository.SemanticGraphQuery) (*repository.SemanticGraphSnapshot, error) {
	s.graphInput = input
	return s.snapshot, s.graphErr
}

func (s *semanticStoreStub) SemanticGraphNodeDetail(_ context.Context, input repository.SemanticGraphNodeDetailInput) (*repository.SemanticGraphNode, error) {
	s.detailInput = input
	return s.detail, s.detailErr
}
