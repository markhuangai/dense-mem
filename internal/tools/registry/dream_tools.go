package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func dreamingStatusTool(deps Dependencies) Tool {
	return Tool{
		Name:        "dreaming_status",
		Description: "Inspect the effective dreaming-cycle config and latest run status for the caller's team. Dreams are hypotheses, not facts.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			_ = input
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			res, err := deps.Dreams.Status(ctx, profileID)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func runDreamingCycleTool(deps Dependencies) Tool {
	return Tool{
		Name:        "run_dreaming_cycle",
		Description: "Manually run the fixed reflect -> re-evaluate -> dream cycle chain for the caller's team. Disabled phases are skipped unless explicitly overridden. This never promotes facts directly.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reflect_enabled":    map[string]any{"type": "boolean"},
				"reevaluate_enabled": map[string]any{"type": "boolean"},
				"dream_enabled":      map[string]any{"type": "boolean"},
				"max_outputs":        map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			var req dreamservice.RunCycleRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("run_dreaming_cycle: invalid input: %w", err)
			}
			req.Manual = true
			res, err := deps.Dreams.RunCycle(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func listDreamsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "list_dreams",
		Description: "List reviewable dream hypotheses for the caller's team. Use this when the user asks to inspect or resolve dreams.",
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
		Description: "Fetch one dream hypothesis with source references. Dreams are assumptions and require user feedback before promotion.",
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
		Description: "Apply user feedback to a dream hypothesis. Use promote_candidate only after the user confirms the hypothesis should enter normal memory review; it creates evidence and then relies on existing verification/promotion gates.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"dream_id", "decision"},
			"properties": map[string]any{
				"dream_id": schemaString("Dream id.", 128),
				"decision": schemaEnum([]string{"reinforce", "stale", "reject", "promote_candidate"}),
				"feedback": schemaString("User feedback or confirmation text.", 1024),
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
