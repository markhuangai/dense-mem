package registry

func v2RecallMemoryInputSchema() map[string]any {
	query := schemaString("Natural-language recall query.", 512)
	query["minLength"] = 1
	return v2ContractInput([]string{"query"}, map[string]any{
		"query":                  query,
		"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"valid_at":               v2NullableDateTime("Real-world recall time."),
		"known_at":               v2NullableDateTime("System-knowledge recall time."),
		"known_evidence_ids":     v2StringArraySchema("Evidence UUID already seen.", 200, 128),
		"known_relationship_ids": v2StringArraySchema("Relationship UUID already seen.", 200, 128),
		"expand_from_entity_ids": v2StringArraySchema("Entity UUID for focused expansion.", 50, 128),
	})
}

func v2RecallMemoryOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"recall_id", "results", "discovery_paths", "discovery_guidance", "related_hypotheses"},
		map[string]any{
			"recall_id":          schemaString("Recall event ID.", 128),
			"results":            v2Array(v2RecallResultSchema(), 0, 50),
			"discovery_paths":    v2Array(v2RecallDiscoveryPathSchema(), 0, 50),
			"discovery_guidance": schemaString("Bounded follow-up guidance.", 1000),
			"related_hypotheses": v2Array(v2HypothesisSummarySchema(), 0, 20),
		},
	)
}

func v2RecallResultSchema() map[string]any {
	return v2ClosedObject(
		[]string{"evidence_id", "context"},
		map[string]any{
			"evidence_id": schemaString("Evidence ID.", 128),
			"context":     schemaString("Bounded evidence context.", 2000),
		},
	)
}
