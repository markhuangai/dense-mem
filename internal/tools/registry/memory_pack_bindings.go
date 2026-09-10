package registry

import (
	"context"
	"fmt"

	memorypackapp "github.com/markhuangai/dense-mem/internal/memorypack"
)

func bindMemoryPackTool(tool Tool, deps Dependencies) Tool {
	if tool.Name != ToolExportMemoryPack {
		return tool
	}
	tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if deps.MemoryPack == nil {
			return nil, ErrToolUnavailable
		}
		if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
		}
		var req memorypackapp.ExportRequest
		if err := remapInput(input, &req); err != nil {
			return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
		}
		res, err := deps.MemoryPack.Export(ctx, req)
		if err != nil {
			return nil, err
		}
		return exportMemoryPackContractOutput(res), nil
	}
	return tool
}
