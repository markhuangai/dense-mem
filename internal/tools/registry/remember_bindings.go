package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
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
		// The registry does not receive the JSON-RPC envelope, so retain the
		// exact logical tools/call params that reached the Remember binding.
		requestBody, _ := json.Marshal(map[string]any{"name": ToolRemember, "arguments": input})
		capture := rememberapp.NewDiagnosticCapture(requestBody)
		capture.SetResponseProjector(func(result map[string]any, isError bool) ([]byte, error) {
			return json.Marshal(ToolCallerResponse(result, isError))
		})
		ctx = rememberapp.WithDiagnosticCapture(ctx, capture)
		res, err := deps.Remember.Remember(ctx, req)
		if err != nil {
			validation := wrapRememberValidationError(err)
			if _, ok := ContractValidationResultFromError(validation); ok {
				return nil, validation
			}
			mapped := rememberToolResultError(ctx, err)
			return nil, mapped
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

// ToolCallerResponse is the canonical MCP response envelope retained in
// diagnostics when a Remember failure occurs.
func ToolCallerResponse(result map[string]any, isError bool) map[string]any {
	payload, err := json.Marshal(result)
	if err != nil {
		return map[string]any{"content": []any{}, "structuredContent": result, "isError": isError}
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(payload)}},
		"structuredContent": result,
		"isError":           isError,
	}
}
