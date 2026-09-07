package registry

import (
	"context"
	"fmt"
	"strings"
)

func bindRememberTool(tool Tool, deps Dependencies) Tool {
	if tool.Name != ToolRemember {
		return tool
	}
	tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if deps.Remember == nil {
			return nil, ErrToolUnavailable
		}
		if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("remember: invalid input: %w", err)
		}
		req, err := rememberRequestFromContractInput(input)
		if err != nil {
			return nil, fmt.Errorf("remember: invalid input: %w", err)
		}
		res, err := deps.Remember.Remember(ctx, req)
		if err != nil {
			validation := wrapRememberValidationError(err)
			if _, ok := ContractValidationResultFromError(validation); ok {
				return nil, validation
			}
			return nil, rememberToolResultError(ctx, err)
		}
		result, err := structToMap(res)
		if err != nil {
			return nil, err
		}
		if state := strings.TrimSpace(fmt.Sprint(result["processing_state"])); state == "failed" {
			return nil, NewToolResultError(result)
		}
		return result, nil
	}
	return tool
}
