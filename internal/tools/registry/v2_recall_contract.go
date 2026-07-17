package registry

import "github.com/markhuangai/dense-mem/internal/domain"

func v2RecallMemoryInputSchema() map[string]any {
	return v2ContractInput([]string{"query"}, map[string]any{
		"query":                  schemaString("Natural-language recall query.", 512),
		"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"valid_at":               map[string]any{"type": "string", "format": "date-time"},
		"known_at":               map[string]any{"type": "string", "format": "date-time"},
		"known_evidence_ids":     v2StringArraySchema("Evidence UUID already seen.", 200, 128),
		"known_relationship_ids": v2StringArraySchema("Relationship UUID already seen.", 200, 128),
		"expand_from_entity_ids": v2StringArraySchema("Entity UUID for focused expansion.", 50, 128),
		"include_evidence":       map[string]any{"type": "boolean"},
		"use_communities":        map[string]any{"type": "boolean"},
	})
}

func v2RecallMemoryOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"recall_id":          schemaString("Recall event ID.", 128),
			"results":            map[string]any{"type": "array", "items": v2RecallResultSchema()},
			"discovery_guidance": schemaString("Bounded follow-up guidance.", 1000),
			"degradation":        v2DegradationSchema(),
			"search_state":       schemaEnum(domain.V2SearchProjectionStates()),
		},
	}
}

func v2RecallResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"evidence_id":      schemaString("Evidence ID.", 128),
			"relationship_ids": v2StringArraySchema("Relationship ID.", 50, 128),
			"score":            map[string]any{"type": "number"},
			"rank":             map[string]any{"type": "integer"},
			"context":          schemaString("Bounded evidence context.", 2000),
		},
	}
}
