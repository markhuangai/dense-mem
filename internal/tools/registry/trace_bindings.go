package registry

import (
	"context"
	"fmt"

	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

func bindTraceTool(tool Tool, deps Dependencies) Tool {
	if tool.Name != ToolTraceMemory {
		return tool
	}
	tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if deps.Context == nil {
			return nil, ErrToolUnavailable
		}
		if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("trace_memory: invalid input: %w", err)
		}
		var req traceapp.TraceRequest
		if err := remapInput(input, &req); err != nil {
			return nil, fmt.Errorf("trace_memory: invalid input: %w", err)
		}
		res, err := deps.Context.Trace(ctx, "", req)
		if err != nil {
			return nil, err
		}
		if res != nil && res.Semantic != nil {
			return traceContractOutput(res.Semantic)
		}
		return structToMap(res)
	}
	return tool
}
