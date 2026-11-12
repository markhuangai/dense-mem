package mcp

import (
	"encoding/json"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyCatalogListAndCallRetireFactualGraph(t *testing.T) {
	reg, err := registry.BuildActive(registry.Dependencies{})
	require.NoError(t, err)
	server := NewServerWithScopes(reg, "team-a", []string{"read", "write"}, nil)
	listed := conformanceRPC(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	require.Nil(t, listed.Error)
	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(listed.Result, &result))
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		for _, field := range []string{"relationships", "entities", "predicates", "relationship_id", "relationship_ids", "expand_from_entity_ids", "community_limit"} {
			require.NotContains(t, properties, field)
		}
	}
	for _, name := range []string{"remember", "recall_memory", "trace_memory", "export_memory_pack", "retract_evidence"} {
		require.True(t, names[name], name)
	}
	for _, name := range []string{"correct_relationship", "query_graph", "list_communities", "list_dreams", "eval_get_dream"} {
		require.False(t, names[name], name)
		call := conformanceRPC(t, server, toolCallRequest(t, 2, name, map[string]any{}))
		require.NotNil(t, call.Error)
		require.Equal(t, errCodeMethodNotFound, call.Error.Code)
	}
	retired := map[string]any{"target_space": "team_shared", "idempotency_key": "retired", "evidence": []any{map[string]any{"content": "Exact user Evidence."}}, "relationships": []any{}}
	rejected := conformanceRPC(t, server, toolCallRequest(t, 3, "remember", retired))
	require.NotNil(t, rejected.Error)
	require.Equal(t, errCodeInvalidParams, rejected.Error.Code)
}
