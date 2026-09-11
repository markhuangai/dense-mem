//go:build evaluation

package registry

import (
	"context"
	"strings"
)

const (
	maxEvaluationPageSize = 500
)

func evalListKnowledgeRefsTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_list_knowledge_refs",
		Description: "Page through authenticated-team knowledge references for evaluation. Content is included unless metadata_only=true.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"type"},
			"properties": map[string]any{
				"type":          schemaEnum(evalListKnowledgeRefTypes(deps)),
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": maxEvaluationPageSize},
				"cursor":        schemaString("Opaque cursor from a previous response.", 512),
				"status":        schemaString("Optional lifecycle status filter.", 64),
				"metadata_only": map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
			kind := strings.ToLower(stringInput(input["type"]))
			limit, _ := intInput(input["limit"])
			metadataOnly, _ := input["metadata_only"].(bool)
			if deps.EvaluationBindings.Application == nil {
				return nil, ErrToolUnavailable
			}
			out, err := deps.EvaluationBindings.Application.ListKnowledgeRefs(ctx, teamID, kind, limit, stringInput(input["cursor"]), stringInput(input["status"]), metadataOnly)
			return out, translateEvaluationError(err)
		},
	}
}

func evalListKnowledgeRefTypes(deps Dependencies) []string {
	types := make([]string, 0, 6)
	if deps.Dreams != nil {
		types = append(types, "dream")
	}
	if deps.EvaluationBindings.Application != nil || deps.Evaluation != nil {
		types = append(types, "evidence", "relationship", "entity", "value", "hypothesis")
	}
	return types
}

func boolInputOrDefault(value any, fallback bool) bool {
	parsed, ok := value.(bool)
	if !ok {
		return fallback
	}
	return parsed
}
