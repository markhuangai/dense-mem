package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/service/contextservice"
)

func traceMemoryTool(deps Dependencies) Tool {
	return Tool{
		Name:        "trace_memory",
		Description: "Expand one fact or claim into bounded evidence and lineage. Use after recall when the answer needs supporting fragments, promotion lineage, contradiction links, or supersession history. This is not free graph traversal.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"type", "id"},
			"properties": map[string]any{
				"type":              schemaEnum([]string{"fact", "claim"}),
				"id":                schemaString("Fact or Claim ID to trace.", 128),
				"max_related":       map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "description": "Maximum related conflict/supersession neighbors. Defaults to 10."},
				"include_fragments": map[string]any{"type": "boolean", "description": "Include supporting SourceFragment content. Defaults to true."},
			},
			"additionalProperties": false,
		},
		OutputSchema:   traceMemoryResultSchema(),
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Context == nil {
				return nil, ErrToolUnavailable
			}
			var req contextservice.TraceRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("trace_memory: invalid input: %w", err)
			}
			if req.Type == "" {
				return nil, errors.New("trace_memory: type is required")
			}
			if req.ID == "" {
				return nil, errors.New("trace_memory: id is required")
			}
			res, err := deps.Context.Trace(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func assembleContextTool(deps Dependencies) Tool {
	return Tool{
		Name:        "assemble_context",
		Description: "Build a bounded prompt-ready Dense-Mem context block plus structured items. Use before answering when prior memory may matter and the host needs facts, claims, fragments, clarifications, and source IDs in one response.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":            schemaString("Natural-language context query.", 512),
				"limit":            map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "description": "Maximum recall hits. Defaults to 5."},
				"max_chars":        map[string]any{"type": "integer", "minimum": 1000, "maximum": 8000, "description": "Maximum context_block characters. Defaults to 4000."},
				"include_evidence": map[string]any{"type": "boolean", "description": "Include supporting SourceFragment content when available. Defaults to true."},
				"valid_at":         map[string]any{"type": "string", "format": "date-time", "description": "Optional real-world validity time for temporal recall filtering."},
				"known_at":         map[string]any{"type": "string", "format": "date-time", "description": "Optional system knowledge time for temporal recall filtering."},
			},
			"additionalProperties": false,
		},
		OutputSchema:   assembleContextResultSchema(),
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Context == nil {
				return nil, ErrToolUnavailable
			}
			var req contextservice.AssembleRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("assemble_context: invalid input: %w", err)
			}
			if req.Query == "" {
				return nil, errors.New("assemble_context: query is required")
			}
			res, err := deps.Context.Assemble(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func traceMemoryResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"anchor": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":  schemaEnum([]string{"fact", "claim"}),
					"fact":  factObjectSchema(),
					"claim": claimObjectSchema(),
				},
			},
			"promoted_from_claim":  claimObjectSchema(),
			"supporting_fragments": map[string]any{"type": "array", "items": fragmentObjectSchema()},
			"related":              map[string]any{"type": "array", "items": relatedMemorySchema()},
			"edges":                map[string]any{"type": "array", "items": traceEdgeSchema()},
			"missing_fragment_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func assembleContextResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":          map[string]any{"type": "string"},
			"context_block":  map[string]any{"type": "string"},
			"items":          map[string]any{"type": "array", "items": contextItemSchema()},
			"clarifications": clarificationArraySchema(),
			"related_dreams": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"truncated":      map[string]any{"type": "boolean"},
		},
	}
}

func relatedMemorySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":     schemaEnum([]string{"fact", "claim"}),
			"relation": map[string]any{"type": "string"},
			"fact":     factObjectSchema(),
			"claim":    claimObjectSchema(),
		},
	}
}

func traceEdgeSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from_type":    map[string]any{"type": "string"},
			"from_id":      map[string]any{"type": "string"},
			"relationship": map[string]any{"type": "string"},
			"to_type":      map[string]any{"type": "string"},
			"to_id":        map[string]any{"type": "string"},
		},
	}
}

func contextItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":               schemaEnum([]string{"fact", "claim", "fragment"}),
			"id":                 map[string]any{"type": "string"},
			"score":              map[string]any{"type": "number"},
			"fact":               factObjectSchema(),
			"claim":              claimObjectSchema(),
			"fragment":           fragmentObjectSchema(),
			"evidence_fragments": map[string]any{"type": "array", "items": fragmentObjectSchema()},
		},
	}
}
