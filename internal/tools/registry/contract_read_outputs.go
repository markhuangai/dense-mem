package registry

import "github.com/markhuangai/dense-mem/internal/domain"

func entityHandleSchema() map[string]any {
	return closedObject(
		[]string{"entity_id", "name"},
		map[string]any{
			"entity_id": schemaString("Entity ID.", 128),
			"name":      schemaString("Bounded Entity display name.", 256),
		},
	)
}

func valueHandleSchema() map[string]any {
	return closedObject(
		[]string{"value_id", "type", "value"},
		map[string]any{
			"value_id": schemaString("Value ID.", 128),
			"type":     schemaEnum(domain.ValueTypes()),
			"value":    map[string]any{"type": []any{"string", "number", "boolean"}},
			"display":  schemaString("Optional display form.", 1024),
			"unit":     schemaString("Optional unit.", 128),
		},
	)
}

func semanticObjectSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{entityHandleSchema(), valueHandleSchema()},
	}
}

func recallDiscoveryPathSchema() map[string]any {
	return closedObject(
		[]string{"relationships", "evidence_ids"},
		map[string]any{
			"relationships": array(recallRelationshipSchema(), 1, 20),
			"evidence_ids":  stringArraySchema("Evidence ID supporting this discovery path.", 50, 128),
		},
	)
}

func recallRelationshipSchema() map[string]any {
	return closedObject(
		[]string{"relationship_id", "subject", "predicate", "object", "polarity"},
		map[string]any{
			"relationship_id": schemaString("Relationship ID.", 128),
			"subject":         entityHandleSchema(),
			"predicate":       schemaString("Registered predicate key.", 128),
			"object":          semanticObjectSchema(),
			"polarity":        schemaEnum([]string{"+", "-"}),
		},
	)
}

func recallConflictSchema() map[string]any {
	return closedObject(
		[]string{"conflict_id", "version", "kind", "status", "question", "positions", "positions_truncated"},
		map[string]any{
			"conflict_id":           schemaString("Conflict case ID.", 128),
			"version":               map[string]any{"type": "integer", "minimum": 1},
			"kind":                  schemaString("Conflict kind.", 128),
			"status":                schemaEnum([]string{"open", "overdue", "resolved"}),
			"question":              schemaString("Bounded deterministic conflict question.", 512),
			"review_due_at":         nullableDateTime("Conflict review deadline."),
			"effective_at":          nullableDateTime("Resolved effective time."),
			"effective_time_basis":  nullableString("Resolved effective time basis.", 64),
			"preferred_position_id": nullableString("Preferred position when resolved.", 128),
			"positions":             array(recallConflictPositionSchema(), 0, 10),
			"positions_truncated":   map[string]any{"type": "boolean"},
		},
	)
}

func recallConflictPositionSchema() map[string]any {
	return closedObject(
		[]string{"position_id", "disposition", "relationship_ids", "owner_profile_ids", "result_evidence_ids"},
		map[string]any{
			"position_id":         schemaString("Conflict position ID.", 128),
			"disposition":         schemaEnum([]string{"candidate", "preferred", "suppressed_current"}),
			"relationship_ids":    stringArraySchema("Relationship ID in this position.", 20, 128),
			"owner_profile_ids":   stringArraySchema("Owner profile ID in this position.", 20, 128),
			"result_evidence_ids": stringArraySchema("Returned evidence ID for this position.", 50, 128),
		},
	)
}

func hypothesisSummarySchema() map[string]any {
	return closedObject(
		[]string{
			"hypothesis_id", "subject_entity_id", "predicate_key", "statement", "status",
			"source_relationship_ids", "generator_kind", "generator_version", "created_at",
		},
		map[string]any{
			"hypothesis_id":           schemaString("Hypothesis ID.", 128),
			"subject_entity_id":       schemaString("Existing subject Entity ID.", 128),
			"predicate_key":           schemaString("Registered predicate key.", 128),
			"object_entity_id":        nullableString("Existing object Entity ID.", 128),
			"object_value_id":         nullableString("Existing object Value ID.", 128),
			"statement":               schemaString("Bounded hypothesis statement.", 1000),
			"status":                  schemaEnum(domain.HypothesisStatuses()),
			"source_relationship_ids": stringArraySchema("Source Relationship ID.", 200, 128),
			"generator_kind":          schemaEnum([]string{"deterministic", "provider"}),
			"generator_version":       schemaString("Dream generator version.", 128),
			"created_at":              map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func hypothesisSchema() map[string]any {
	return closedObject(
		[]string{
			"hypothesis_id", "source_owner_profile_ids", "subject_entity_id", "predicate_key",
			"statement", "rationale", "likelihood", "confidence", "source_relationship_ids",
			"source_candidate_relationship_ids", "source_versions", "generator_kind",
			"generator_version", "status", "created_at",
		},
		map[string]any{
			"hypothesis_id":                     schemaString("Hypothesis ID.", 128),
			"source_owner_profile_ids":          stringArraySchema("Source owner profile ID.", 200, 128),
			"subject_entity_id":                 schemaString("Existing subject Entity ID.", 128),
			"predicate_key":                     schemaString("Registered predicate key.", 128),
			"object_entity_id":                  nullableString("Existing object Entity ID.", 128),
			"object_value_id":                   nullableString("Existing object Value ID.", 128),
			"statement":                         schemaString("Bounded hypothesis statement.", 1000),
			"rationale":                         schemaString("Bounded generation rationale.", 2000),
			"likelihood":                        map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"confidence":                        map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"source_relationship_ids":           stringArraySchema("Source Relationship ID.", 200, 128),
			"source_candidate_relationship_ids": stringArraySchema("Source candidate Relationship ID.", 200, 128),
			"source_versions":                   versionMapSchema(),
			"generator_kind":                    schemaEnum([]string{"deterministic", "provider"}),
			"generator_version":                 schemaString("Dream generator version.", 128),
			"status":                            schemaEnum(domain.HypothesisStatuses()),
			"created_at":                        map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func versionMapSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "integer", "minimum": 1},
		"maxProperties":        200,
		"x-bounded-map":        true,
	}
}

func correctionRelationshipChangeSchema() map[string]any {
	return closedObject(
		[]string{"relationship_id", "before_version", "after_subject_entity_id"},
		map[string]any{
			"relationship_id":         schemaString("Caller-owned Relationship ID.", 128),
			"before_version":          map[string]any{"type": "integer", "minimum": 1},
			"after_subject_entity_id": schemaString("Entity ID after correction.", 128),
			"after_object_entity_id":  nullableString("Object Entity ID after correction.", 128),
		},
	)
}

func correctionEntityCandidateSchema() map[string]any {
	return closedObject(
		[]string{"entity_id", "disposition"},
		map[string]any{
			"entity_id":   schemaString("New or reused Entity candidate ID.", 128),
			"disposition": schemaEnum([]string{"new", "reused"}),
		},
	)
}

func crossProfileReferenceSchema() map[string]any {
	return closedObject(
		[]string{"reference_id", "source_relationship_id", "target_relationship_id", "unchanged"},
		map[string]any{
			"reference_id":           schemaString("Cross-profile reference ID.", 128),
			"source_relationship_id": schemaString("Caller-owned source Relationship ID.", 128),
			"target_relationship_id": schemaString("Same-team target Relationship ID.", 128),
			"unchanged":              map[string]any{"type": "boolean"},
		},
	)
}

func traceRelationshipSchema() map[string]any {
	return closedObject(
		[]string{"relationship_id"},
		map[string]any{
			"relationship_id":     schemaString("Relationship ID.", 128),
			"owner_profile_id":    schemaString("Relationship owner profile ID.", 128),
			"subject_entity_id":   schemaString("Subject Entity ID.", 128),
			"subject_name":        schemaString("Subject Entity name.", 256),
			"predicate_key":       schemaString("Registered predicate key.", 128),
			"predicate_version":   map[string]any{"type": "integer", "minimum": 1},
			"object_entity_id":    nullableString("Object Entity ID.", 128),
			"object_value_id":     nullableString("Object Value ID.", 128),
			"polarity":            schemaEnum([]string{"+", "-"}),
			"tier":                schemaEnum(domain.RelationshipTiers()),
			"relationship_status": schemaEnum(domain.RelationshipStatuses()),
			"version":             map[string]any{"type": "integer", "minimum": 1},
			"valid_from":          nullableDateTime("Real-world validity start."),
			"valid_to":            nullableDateTime("Real-world validity end."),
			"created_at":          map[string]any{"type": "string", "format": "date-time"},
			"updated_at":          map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceObservationSchema() map[string]any {
	return closedObject(
		[]string{"observation_id", "ingest_id", "placement_item_id"},
		map[string]any{
			"observation_id":     schemaString("Observation ID.", 128),
			"relationship_id":    nullableString("Relationship ID when resolved.", 128),
			"ingest_id":          schemaString("Placement run ID.", 128),
			"placement_item_id":  schemaString("Placement item ID.", 128),
			"subject_ref":        schemaString("Submitted subject ref.", 128),
			"original_predicate": schemaString("Original predicate wording.", 128),
			"object_ref":         schemaString("Submitted object ref.", 128),
			"polarity":           schemaEnum([]string{"+", "-"}),
			"created_at":         map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceEvidenceSupportSchema() map[string]any {
	return closedObject(
		[]string{"support_id", "relationship_id", "fragment_id", "span_start", "span_end"},
		map[string]any{
			"support_id":            schemaString("Evidence support ID.", 128),
			"relationship_id":       schemaString("Relationship ID.", 128),
			"observation_id":        schemaString("Observation ID.", 128),
			"verification_event_id": schemaString("Verification event ID.", 128),
			"fragment_id":           schemaString("Evidence fragment ID.", 128),
			"source_group_key":      schemaString("Derived source group key.", 256),
			"span_start":            map[string]any{"type": "integer", "minimum": 0},
			"span_end":              map[string]any{"type": "integer", "minimum": 0},
			"quote":                 schemaString("Exact evidence quote.", 999),
			"authority":             schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
			"created_at":            map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceSupportDecisionSchema() map[string]any {
	return closedObject(
		[]string{"support_decision_id", "support_id", "relationship_id", "decision", "created_at"},
		map[string]any{
			"support_decision_id": schemaString("Support decision ID.", 128),
			"support_id":          schemaString("Evidence support ID.", 128),
			"relationship_id":     schemaString("Relationship ID.", 128),
			"actor_profile_id":    schemaString("Decision actor profile ID.", 128),
			"decision":            schemaEnum([]string{"grant", "revoke", "reinstate"}),
			"reason":              schemaString("Bounded decision reason.", 1000),
			"created_at":          map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceEvidenceFragmentSchema() map[string]any {
	return closedObject(
		[]string{"fragment_id", "ingest_id", "evidence_index", "content_hash", "content_truncated"},
		map[string]any{
			"fragment_id":       schemaString("Evidence fragment ID.", 128),
			"ingest_id":         schemaString("Placement run ID.", 128),
			"evidence_index":    map[string]any{"type": "integer", "minimum": 0, "maximum": 19},
			"content":           schemaString("Optional bounded evidence content.", 999),
			"content_hash":      schemaString("Evidence content hash.", 128),
			"content_truncated": map[string]any{"type": "boolean"},
			"source_type":       schemaEnum([]string{"conversation", "document", "observation", "manual"}),
			"source":            schemaString("Bounded provenance locator.", 256),
			"created_at":        map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceVerificationSchema() map[string]any {
	return closedObject(
		[]string{"verification_event_id", "observation_id", "evidence_verdict", "created_at"},
		map[string]any{
			"verification_event_id": schemaString("Verification event ID.", 128),
			"observation_id":        schemaString("Observation ID.", 128),
			"evidence_verdict":      schemaEnum([]string{"entailed", "contradicted", "insufficient"}),
			"confidence":            nullableNumber("Verifier confidence.", 0, 1),
			"rationale":             schemaString("Bounded verifier rationale.", 1000),
			"created_at":            map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceTransitionSchema() map[string]any {
	return closedObject(
		[]string{"transition_id", "relationship_id", "to_tier", "to_status", "reason", "created_at"},
		map[string]any{
			"transition_id":   schemaString("Lifecycle transition ID.", 128),
			"relationship_id": schemaString("Relationship ID.", 128),
			"from_tier":       nullableString("Prior Relationship tier.", 64),
			"from_status":     nullableString("Prior Relationship status.", 64),
			"to_tier":         schemaEnum(domain.RelationshipTiers()),
			"to_status":       schemaEnum(domain.RelationshipStatuses()),
			"reason":          schemaString("Bounded transition reason.", 1000),
			"created_at":      map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceConflictSchema() map[string]any {
	return recallConflictSchema()
}

func traceIdentityCorrectionSchema() map[string]any {
	return closedObject(
		[]string{"correction_event_id", "operation", "source_entity_id", "created_at"},
		map[string]any{
			"correction_event_id":   schemaString("Identity correction event ID.", 128),
			"operation":             schemaEnum(domain.EntityCorrectionActions()),
			"source_entity_id":      schemaString("Source Entity ID.", 128),
			"target_entity_id":      nullableString("Merge target Entity ID.", 128),
			"owned_observation_ids": stringArraySchema("Affected caller-owned observation ID.", 200, 128),
			"reason":                schemaString("Bounded correction reason.", 1000),
			"created_at":            map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func semanticNodeSchema() map[string]any {
	return closedObject(
		[]string{"id", "kind", "name"},
		map[string]any{
			"id":   schemaString("Entity or Value ID.", 128),
			"kind": schemaEnum([]string{"entity", "value"}),
			"name": schemaString("Bounded semantic node label.", 256),
		},
	)
}

func semanticEdgeSchema() map[string]any {
	return closedObject(
		[]string{"relationship_id", "source_id", "target_id", "predicate", "polarity"},
		map[string]any{
			"relationship_id": schemaString("Relationship ID.", 128),
			"source_id":       schemaString("Source Entity ID.", 128),
			"target_id":       schemaString("Target Entity or Value ID.", 128),
			"predicate":       schemaString("Registered predicate key.", 128),
			"polarity":        schemaEnum([]string{"+", "-"}),
		},
	)
}
