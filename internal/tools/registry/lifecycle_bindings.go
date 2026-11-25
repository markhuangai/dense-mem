package registry

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/lifecycle"
)

func bindLifecycleTool(tool Tool, deps Dependencies) Tool {
	switch tool.Name {
	case ToolRetractEvidence:
		tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
			if deps.LifecycleBindings.Service == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
				return nil, fmt.Errorf("retract_evidence: invalid input: %w", err)
			}
			var req lifecycle.RetractEvidenceRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("retract_evidence: invalid input: %w", err)
			}
			res, err := deps.LifecycleBindings.Service.RetractEvidence(ctx, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		}

	}
	return tool
}
