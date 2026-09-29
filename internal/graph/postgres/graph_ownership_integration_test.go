//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	graphapp "github.com/markhuangai/dense-mem/internal/graph"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

func TestGraphOwnershipNormalizedServiceAndAdapter(t *testing.T) {
	f := newGraphOwnershipFixture(t)
	ctx := context.Background()
	service := graphapp.New(f.store)

	for _, test := range []struct {
		name, scope string
		depth       int
		edgeIDs     []string
	}{
		{"overview", "unexpected", 0, f.edgeIDs},
		{"local default", " LOCAL ", -1, f.edgeIDs[:2]},
		{"local capped", "local", 99, f.edgeIDs[:5]},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := graphcontract.Query{
				TeamID: f.teamID, Scope: test.scope, Query: " GRAPH NODE ",
				Types:      []string{"entities", "entity", "unknown", "values"},
				AnchorType: "ENTITIES", AnchorID: " " + f.nodeIDs[0] + " ",
				Depth: test.depth, Limit: 181,
			}
			direct, err := f.store.SemanticGraph(ctx, input)
			require.NoError(t, err)
			viaService, err := service.Graph(ctx, f.teamID, graphapp.Query{
				Scope: input.Scope, Query: input.Query, Types: input.Types,
				AnchorType: input.AnchorType, AnchorID: input.AnchorID,
				Depth: input.Depth, Limit: input.Limit,
			})
			require.NoError(t, err)
			assert.Equal(t, direct.Scope, viaService.Scope)
			assert.Equal(t, direct.Query, viaService.Query)
			assert.Equal(t, direct.Depth, viaService.Depth)
			assert.Equal(t, direct.Limit, viaService.Limit)
			assert.Equal(t, test.edgeIDs, semanticGraphEdgeIDs(direct.Edges))
			serviceIDs := make([]string, 0, len(viaService.Edges))
			for _, edge := range viaService.Edges {
				serviceIDs = append(serviceIDs, edge.ID)
			}
			assert.Equal(t, test.edgeIDs, serviceIDs)
		})
	}

	detail, err := f.store.SemanticGraphNodeDetail(ctx, graphcontract.NodeDetailInput{
		TeamID: f.teamID, NodeType: "ENTITIES", NodeID: f.nodeIDs[0],
	})
	require.NoError(t, err)
	viaService, err := service.NodeDetail(ctx, f.teamID, "ENTITIES", f.nodeIDs[0])
	require.NoError(t, err)
	assert.Equal(t, f.nodeIDs[0], detail.ID)
	assert.Equal(t, "Graph Node 0", detail.Title)
	assert.Equal(t, detail.ID, viaService.ID)
	assert.Equal(t, detail.Title, viaService.Title)

	fallback, err := f.store.SemanticGraph(ctx, graphcontract.Query{TeamID: f.teamID, Types: []string{"unknown"}})
	require.NoError(t, err)
	assert.Equal(t, f.edgeIDs, semanticGraphEdgeIDs(fallback.Edges))
}
