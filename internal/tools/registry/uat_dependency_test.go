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
			name: ToolTraceMemory,
			args: map[string]any{
				"evidence_id": "00000000-0000-0000-0000-000000000001",
			},
		},
		{
			name: ToolExportMemoryPack,
			args: map[string]any{
				"name":         "PostgreSQL pack",
				"evidence_ids": []any{"00000000-0000-0000-0000-000000000001"},
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
