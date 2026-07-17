package registry

func v2TraceMemoryInputSchema() map[string]any {
	return v2ContractInput([]string{"relationship_id"}, map[string]any{
		"relationship_id":          schemaString("Same-team Relationship ID.", 128),
		"include_evidence_content": map[string]any{"type": "boolean"},
		"include_verification":     map[string]any{"type": "boolean"},
		"include_transitions":      map[string]any{"type": "boolean"},
		"max_depth":                map[string]any{"type": "integer", "minimum": 0, "maximum": 4},
		"max_edges":                map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"predicate_keys":           v2StringArraySchema("Registered predicate key filter.", 30, 128),
		"topic":                    v2NullableString("Optional graph-context topic.", 256),
		"min_relevance":            v2NullableNumber("Optional graph-context relevance threshold.", 0, 1),
	})
}

func v2TraceMemoryOutputSchema() map[string]any {
	required := []string{
		"relationship", "observations", "evidence_supports", "support_decision_events",
		"evidence_fragments", "verification_events", "transitions", "conflicts",
		"cross_profile_references", "identity_corrections", "supersession_lineage",
		"semantic_nodes", "semantic_edges", "visited_entity_ids", "stopped_reason",
	}
	return v2ClosedObject(required, map[string]any{
		"relationship":             v2TraceRelationshipSchema(),
		"observations":             v2Array(v2TraceObservationSchema(), 0, 500),
		"evidence_supports":        v2Array(v2TraceEvidenceSupportSchema(), 0, 500),
		"support_decision_events":  v2Array(v2TraceSupportDecisionSchema(), 0, 500),
		"evidence_fragments":       v2Array(v2TraceEvidenceFragmentSchema(), 0, 500),
		"verification_events":      v2Array(v2TraceVerificationSchema(), 0, 500),
		"transitions":              v2Array(v2TraceTransitionSchema(), 0, 500),
		"conflicts":                v2Array(v2TraceConflictSchema(), 0, 500),
		"cross_profile_references": v2Array(v2CrossProfileReferenceSchema(), 0, 500),
		"identity_corrections":     v2Array(v2TraceIdentityCorrectionSchema(), 0, 500),
		"supersession_lineage":     v2Array(v2TraceRelationshipSchema(), 0, 500),
		"semantic_nodes":           v2Array(v2SemanticNodeSchema(), 0, 500),
		"semantic_edges":           v2Array(v2SemanticEdgeSchema(), 0, 500),
		"visited_entity_ids":       v2StringArraySchema("Visited Entity ID.", 500, 128),
		"stopped_reason":           v2NullableString("Trace budget stop reason.", 128),
	})
}
