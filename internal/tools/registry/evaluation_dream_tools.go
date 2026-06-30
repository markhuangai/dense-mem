package registry

import (
	"context"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func evalRunDreamCycleTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_run_dream_cycle",
		Description: "Run the dream phase for a team during evaluation so expected dream hypotheses can be exported and scored.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"manual":             map[string]any{"type": "boolean"},
				"reflect_enabled":    map[string]any{"type": "boolean"},
				"reevaluate_enabled": map[string]any{"type": "boolean"},
				"dream_enabled":      map[string]any{"type": "boolean"},
				"max_outputs":        map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			maxOutputs := intInputOrDefault(input["max_outputs"], dreamservice.DefaultMaxOutputs)
			if err := auditEvaluationTool(ctx, deps, "eval_run_dream_cycle", maxOutputs, false, nil); err != nil {
				return nil, err
			}
			req := dreamservice.RunCycleRequest{
				Manual:            boolInputOrDefault(input["manual"], true),
				ReflectEnabled:    boolPtrInput(input["reflect_enabled"]),
				ReevaluateEnabled: boolPtrInput(input["reevaluate_enabled"]),
				DreamEnabled:      boolPtrInput(input["dream_enabled"]),
				MaxOutputs:        maxOutputs,
			}
			result, err := deps.Dreams.RunCycle(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			out, err := structToMap(result)
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	}
}

func evalListDreams(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.Dreams == nil {
		return nil, ErrToolUnavailable
	}
	opts := dreamservice.ListOptions{Limit: limit}
	if cursor, ok := input["cursor"].(string); ok {
		opts.Cursor = cursor
	}
	if status, ok := input["status"].(string); ok {
		opts.Status = status
	}
	dreams, nextCursor, err := deps.Dreams.List(ctx, profileID, opts)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(dreams))
	for _, dream := range dreams {
		item, err := structToMap(dream)
		if err != nil {
			return nil, err
		}
		if metadataOnly {
			stripEvalContent("dream", item)
		}
		items = append(items, item)
	}
	return evalPage(items, nextCursor), nil
}

func dreamRefs(dreams []*domain.Dream) []map[string]any {
	refs := make([]map[string]any, 0, len(dreams))
	for i, dream := range dreams {
		if dream == nil || strings.TrimSpace(dream.DreamID) == "" {
			continue
		}
		refs = append(refs, map[string]any{
			"rank":   i + 1,
			"type":   "dream",
			"id":     dream.DreamID,
			"status": string(dream.Status),
		})
	}
	return refs
}

func boolPtrInput(value any) *bool {
	parsed, ok := value.(bool)
	if !ok {
		return nil
	}
	return &parsed
}
