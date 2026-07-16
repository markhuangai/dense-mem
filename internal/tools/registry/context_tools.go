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
		Description: "Expand one v2 relationship_id into bounded evidence and lineage. Legacy fact/claim type+id inputs remain accepted when the graph-backed context service is wired.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"relationship_id":          schemaString("Relationship ID to trace.", 128),
				"type":                     schemaEnum([]string{"relationship", "fact", "claim"}),
				"id":                       schemaString("Legacy Fact/Claim ID or relationship ID when type=relationship.", 128),
				"max_related":              map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "description": "Maximum related conflict/supersession neighbors. Defaults to 10."},
				"include_fragments":        map[string]any{"type": "boolean", "description": "Include supporting SourceFragment content. Defaults to true."},
				"include_evidence_content": map[string]any{"type": "boolean", "description": "Include semantic evidence content. Defaults to true."},
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
			if req.RelationshipID == "" && (req.Type == "" || req.ID == "") {
				return nil, errors.New("trace_memory: relationship_id or legacy type/id is required")
			}
			res, err := deps.Context.Trace(ctx, profileID, req)
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
					"type":         schemaEnum([]string{"fact", "claim"}),
					"fact":         factObjectSchema(),
					"claim":        claimObjectSchema(),
					"relationship": semanticRelationshipObjectSchema(),
				},
			},
			"promoted_from_claim":  claimObjectSchema(),
			"supporting_fragments": map[string]any{"type": "array", "items": fragmentObjectSchema()},
			"semantic_evidence":    map[string]any{"type": "array", "items": semanticEvidenceObjectSchema()},
			"semantic_supports":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"related":              map[string]any{"type": "array", "items": relatedMemorySchema()},
			"edges":                map[string]any{"type": "array", "items": traceEdgeSchema()},
			"missing_fragment_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
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
