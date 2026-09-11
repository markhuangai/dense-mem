//go:build evaluation

package registry

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/recall"
)

func evalRunRecallCaseTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_run_recall_case",
		Description: "Run one recall/context evaluation case through the current Dense-Mem logic and return ranked refs plus context refs.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"case_id", "query"},
			"properties": map[string]any{
				"case_id":           schemaString("Evaluation case ID.", 256),
				"query":             schemaString("Recall query.", 512),
				"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				"valid_at":          map[string]any{"type": "string", "format": "date-time"},
				"known_at":          map[string]any{"type": "string", "format": "date-time"},
				"include_evidence":  map[string]any{"type": "boolean"},
				"use_communities":   map[string]any{"type": "boolean"},
				"include_dreams":    map[string]any{"type": "boolean"},
				"max_context_chars": map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
			if deps.EvaluationBindings.Application == nil {
				return nil, ErrToolUnavailable
			}
			req, err := evalRecallRequest(input)
			if err != nil {
				return nil, err
			}
			out, err := deps.EvaluationBindings.Application.RunRecallCase(ctx, rawStringInput(input["case_id"]), req,
				rawStringInput(input["valid_at"]), rawStringInput(input["known_at"]),
				boolInputOrDefault(input["include_evidence"], true), boolInput(input["include_dreams"]))
			return out, translateEvaluationError(err)
		},
	}
}

func evalRecallRequest(input map[string]any) (recall.RecallRequest, error) {
	var req recall.RecallRequest
	withoutTimes := make(map[string]any, len(input))
	for key, value := range input {
		if key == "valid_at" || key == "known_at" {
			continue
		}
		withoutTimes[key] = value
	}
	if err := remapInput(withoutTimes, &req); err != nil {
		return req, err
	}
	req.Query = stringInput(input["query"])
	return req, nil
}

func rawStringInput(value any) string {
	parsed, _ := value.(string)
	return parsed
}
