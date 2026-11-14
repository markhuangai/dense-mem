package registry

import (
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestEvidenceOnlyCatalogValidationIdentifiesEvidencePaths(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	for _, test := range []struct {
		name, path string
		evidence   any
	}{
		{name: "missing", path: "/evidence"},
		{name: "empty", path: "/evidence", evidence: []any{}},
		{name: "oversized", path: "/evidence/0/content", evidence: []any{map[string]any{"content": strings.Repeat("x", 16385)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"target_space": "team_shared", "idempotency_key": "key"}
			if test.evidence != nil {
				input["evidence"] = test.evidence
			}
			issues := ValidateContractInputIssues(remember, input, []string{"read", "write"})
			require.NotEmpty(t, issues.Issues)
			require.Equal(t, test.path, issues.Issues[0].Path)
		})
	}
}

func TestEvidenceOnlyCatalogHasOnlyCurrentMemoryContracts(t *testing.T) {
	tools := ContractTools()
	require.Len(t, tools, 7)
	names := map[string]Tool{}
	for _, tool := range tools {
		names[tool.Name] = tool
	}
	for _, name := range []string{"remember", "ingest_session", "retract_evidence", "recall_memory", "trace_memory", "submit_recall_session_feedback", "export_memory_pack"} {
		require.Contains(t, names, name)
	}
	require.NotContains(t, names, "correct_relationship")
	require.Equal(t, "dense-mem.v2.8.0", domain.ContractVersion)
	id := uuid.NewString()
	for name, input := range map[string]map[string]any{
		ToolRemember:         {"target_space": "team_shared", "idempotency_key": "one", "evidence": []any{map[string]any{"content": "Exact Evidence."}}},
		ToolRecallMemory:     {"topic_ids": []any{id}, "topic_limit": 0},
		ToolTraceMemory:      {"evidence_id": id},
		ToolExportMemoryPack: {"name": "Evidence pack", "evidence_ids": []any{id}},
	} {
		require.NoError(t, ValidateContractInput(names[name], input, []string{"read", "write"}))
	}
	for _, field := range []string{"relationships", "entities", "values", "predicates", "proposal"} {
		input := map[string]any{"target_space": "team_shared", "idempotency_key": "one", "evidence": []any{map[string]any{"content": "Exact Evidence."}}, field: []any{}}
		require.Error(t, ValidateContractInput(names[ToolRemember], input, []string{"read", "write"}))
	}
	for _, input := range []map[string]any{{"topic_ids": []any{}}, {"topic_ids": []any{id, id}}, {"topic_ids": []any{"missing"}}, {"query": "question", "topic_limit": 11}, {"query": "question", "known_relationship_ids": []any{id}}} {
		require.Error(t, ValidateContractInput(names[ToolRecallMemory], input, []string{"read"}))
	}
	require.Error(t, ValidateContractInput(names[ToolTraceMemory], map[string]any{"relationship_id": id}, []string{"read"}))
	require.Error(t, ValidateContractInput(names[ToolExportMemoryPack], map[string]any{"name": "old", "relationship_ids": []any{id}}, []string{"read"}))
}
