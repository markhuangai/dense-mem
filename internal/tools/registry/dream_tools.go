package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func listDreamsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "list_dreams",
		Description: "List reviewable dream hypotheses for the caller's team. Treat returned dreams as hypotheses, not memory. Use this when the user asks to inspect or resolve dreams.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				"status": schemaEnum([]string{"proposed", "reinforced", "stale", "rejected", "promoted"}),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			opts := dreamservice.ListOptions{}
			if v, ok := input["limit"].(float64); ok {
				opts.Limit = int(v)
			}
			if v, ok := input["status"].(string); ok {
				opts.Status = v
			}
			dreams, next, err := deps.Dreams.List(ctx, profileID, opts)
			if err != nil {
				return nil, err
			}
			return map[string]any{"dreams": dreams, "next_cursor": next}, nil
		},
	}
}

func getDreamTool(deps Dependencies) Tool {
	return Tool{
		Name:        "get_dream",
		Description: "Fetch one dream hypothesis with source references. Dreams are assumptions and require explicit user evidence or confirmation before promotion.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"dream_id"},
			"properties":           map[string]any{"dream_id": schemaString("Dream id.", 128)},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			id, _ := input["dream_id"].(string)
			if id == "" {
				return nil, errors.New("get_dream: dream_id is required")
			}
			dream, err := deps.Dreams.Get(ctx, profileID, id)
			if err != nil {
				return nil, err
			}
			return structToMap(dream)
		},
	}
}

func resolveDreamFeedbackTool(deps Dependencies) Tool {
	return Tool{
		Name:        "resolve_dream_feedback",
		Description: "Apply evidence-driven feedback to a dream hypothesis. Dreams are uncertain recall hints. Use ignore when no decision was made, reinforce when it remains useful but unconfirmed, reject or stale for manual lifecycle cleanup, confirm_true when prior conversation or user feedback confirms it should enter normal memory placement, and confirm_false when evidence confirms it is not true and correction evidence should enter normal memory placement. promote_candidate is accepted as a backward-compatible alias for confirm_true. Do not confirm based only on model confidence.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"dream_id", "decision"},
			"properties": map[string]any{
				"dream_id": schemaString("Dream id.", 128),
				"decision": schemaEnum([]string{"ignore", "reinforce", "stale", "reject", "confirm_true", "confirm_false", "promote_candidate"}),
				"feedback": schemaString("User feedback, prior-conversation evidence, or correction text. Required for confirm_true, confirm_false, and promote_candidate.", 1024),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			var req dreamservice.ResolveFeedbackRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
			}
			res, err := deps.Dreams.ResolveFeedback(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}
