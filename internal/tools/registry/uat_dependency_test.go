package registry

import (
	"context"
	"errors"

	"testing"
)

func TestBuildActiveExecutableToolsRequireDependencies(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: ToolRemember,
			args: map[string]any{
				"evidence": []any{map[string]any{"content": "remember"}},
			},
		},
		{
			name: ToolRecallMemory,
			args: map[string]any{
				"query": "PostgreSQL",
			},
		},
		{
			name: ToolCorrectRelationship,
			args: map[string]any{
				"action":           "submit",
				"relationship_id":  "relationship-source",
				"expected_version": 1,
				"patch":            map[string]any{"predicate": map[string]any{"key": "works_with"}},
				"supports":         []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 8}},
				"reason":           "predicate resolved incorrectly",
				"idempotency_key":  "correction-1",
			},
		},
		{
			name: ToolTraceMemory,
			args: map[string]any{
				"relationship_id": "relationship-canonical",
			},
		},
		{
			name: ToolExportMemoryPack,
			args: map[string]any{
				"name":             "PostgreSQL pack",
				"relationship_ids": []any{"relationship-canonical"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := reg.Get(tc.name)
			if !ok || tool.Invoke == nil {
				t.Fatalf("tool %s not executable", tc.name)
			}
			_, err := tool.Invoke(context.Background(), "ignored-profile", tc.args)
			if !errors.Is(err, ErrToolUnavailable) {
				t.Fatalf("%s err = %v, want ErrToolUnavailable", tc.name, err)
			}
		})
	}
}
