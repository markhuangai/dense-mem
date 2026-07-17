package registry

func v2TraceMemoryInputSchema() map[string]any {
	return v2ContractInput([]string{"relationship_id"}, map[string]any{
		"relationship_id":          schemaString("Same-team Relationship ID.", 128),
		"include_evidence_content": map[string]any{"type": "boolean"},
		"include_verification":     map[string]any{"type": "boolean"},
		"include_transitions":      map[string]any{"type": "boolean"},
		"max_depth":                map[string]any{"type": "integer", "minimum": 1, "maximum": 4},
		"max_edges":                map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"max_chars":                map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
		"predicate_keys":           v2StringArraySchema("Predicate key filter.", 100, 128),
		"topic":                    schemaString("Optional bounded graph context search topic.", 512),
	})
}

func v2TraceMemoryOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"relationship":      map[string]any{"type": "object"},
			"observations":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"evidence_supports": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"support_decision_events": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "object"},
			},
			"evidence_fragments":  map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"verification_events": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"transitions":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"cross_profile_references": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "object"},
			},
			"identity_corrections": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"supersession_lineage": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"search_documents":     map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"embedding_jobs":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"semantic_nodes":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"semantic_edges":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"visited_entity_ids":   v2StringArraySchema("Visited Entity ID.", 500, 128),
			"degradation":          v2DegradationSchema(),
			"stopped_reason":       schemaString("Trace budget stop reason.", 128),
			"truncated":            map[string]any{"type": "boolean"},
		},
	}
}
