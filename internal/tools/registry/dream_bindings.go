package registry

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func bindDreamTool(tool Tool, deps Dependencies) Tool {
	switch tool.Name {
	case ToolListDreams:
		tool.Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
				return nil, fmt.Errorf("list_dreams: invalid input: %w", err)
			}
			opts := dreamservice.ListOptions{Status: stringInput(input["status"]), Cursor: stringInput(input["cursor"])}
			if limit, ok := intInput(input["limit"]); ok {
				opts.Limit = limit
			}
			dreams, next, err := deps.Dreams.List(ctx, teamID, opts)
			if err != nil {
				return nil, err
			}
			return listDreamsContractOutput(dreams, next), nil
		}
	case ToolGetDream:
		tool.Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
				return nil, fmt.Errorf("get_dream: invalid input: %w", err)
			}
			dream, err := deps.Dreams.Get(ctx, teamID, stringInput(input["hypothesis_id"]))
			if err != nil {
				return nil, err
			}
			return map[string]any{"hypothesis": dreamContractOutput(dream)}, nil
		}
	case ToolResolveDreamFeedback:
		tool.Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
				return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
			}
			req, err := resolveDreamFeedbackRequestFromContractInput(input)
			if err != nil {
				return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
			}
			res, err := deps.Dreams.ResolveFeedback(ctx, teamID, req)
			if err != nil {
				if busy, ok := resolveDreamConfirmationBusyOutcome(err); ok {
					return nil, NewToolResultError(busy)
				}
				return nil, err
			}
			if failure, ok, err := resolveDreamTerminalOutcome(res); err != nil {
				return nil, fmt.Errorf("resolve_dream_feedback: terminal result serialization failed")
			} else if ok {
				return nil, NewToolResultError(failure)
			}
			return resolveDreamFeedbackContractOutput(res), nil
		}
	}
	return tool
}
