package postgres

import (
	"testing"

	"github.com/google/uuid"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeGraphQueryDefaultsAndBounds(t *testing.T) {
	defaults := normalizeSemanticGraphQuery(graphcontract.Query{})
	assert.Equal(t, graphcontract.DefaultDepth, defaults.Depth)
	assert.Equal(t, graphcontract.DefaultLimit, defaults.Limit)

	explicit := normalizeSemanticGraphQuery(graphcontract.Query{Depth: 99, Limit: 181})
	assert.Equal(t, graphcontract.MaxDepth, explicit.Depth)
	assert.Equal(t, 181, explicit.Limit)

	large := normalizeSemanticGraphQuery(graphcontract.Query{Limit: 1_000_000})
	assert.Equal(t, 1_000_000, large.Limit)

	ordered := normalizeSemanticGraphQuery(graphcontract.Query{
		Scope: " LOCAL ", Query: " Project ", Types: []string{"values", "entities", "unknown", "value"},
		AnchorType: "ENTITIES", AnchorID: " id ", Depth: -1, Limit: -1,
	})
	assert.Equal(t, graphcontract.ScopeLocal, ordered.Scope)
	assert.Equal(t, "project", ordered.Query)
	assert.Equal(t, []string{"entity", "value"}, ordered.Types, "adapter retains canonical order")
	assert.Equal(t, "entity", ordered.AnchorType)
	assert.Equal(t, "id", ordered.AnchorID)
	assert.Equal(t, graphcontract.DefaultDepth, ordered.Depth)
	assert.Equal(t, graphcontract.DefaultLimit, ordered.Limit)
	assert.Equal(t, []string{"entity", "value"}, normalizeSemanticGraphQuery(graphcontract.Query{Types: []string{"unknown"}}).Types)
}

func TestValidateGraphQueryRetainsAdapterErrorOrder(t *testing.T) {
	invalidTeam := normalizeSemanticGraphQuery(graphcontract.Query{TeamID: "bad", Scope: "local", AnchorType: "unknown"})
	assert.ErrorContains(t, validateSemanticGraphQuery(invalidTeam), "team_id is required")

	teamID := uuid.NewString()
	missingType := normalizeSemanticGraphQuery(graphcontract.Query{TeamID: teamID, Scope: "local", AnchorType: "unknown", AnchorID: "bad"})
	assert.ErrorContains(t, validateSemanticGraphQuery(missingType), "anchor_type is required")
	missingID := normalizeSemanticGraphQuery(graphcontract.Query{TeamID: teamID, Scope: "local", AnchorType: "entities", AnchorID: "bad"})
	assert.ErrorContains(t, validateSemanticGraphQuery(missingID), "anchor_id is required")

	detail := normalizeSemanticGraphNodeDetailInput(graphcontract.NodeDetailInput{TeamID: "bad", NodeType: "unknown", NodeID: "bad"})
	assert.ErrorContains(t, validateSemanticGraphNodeDetailInput(detail), "team_id is required")
	detail.TeamID = teamID
	assert.ErrorContains(t, validateSemanticGraphNodeDetailInput(detail), "node_type must be entity or value")
	detail.NodeType = graphcontract.NormalizeNodeType("values")
	assert.ErrorContains(t, validateSemanticGraphNodeDetailInput(detail), "node_id is required")
}
