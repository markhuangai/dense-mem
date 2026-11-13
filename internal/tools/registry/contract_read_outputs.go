package registry

func recallConflictSchema() map[string]any { return recallEvidenceConflictSchema() }
func recallEvidenceConflictSchema() map[string]any {
	return closedObject(
		[]string{"conflict_id", "version", "kind", "status", "positions", "positions_truncated"},
		map[string]any{
			"conflict_id":           schemaString("Evidence conflict case ID.", 128),
			"version":               map[string]any{"type": "integer", "minimum": 1},
			"kind":                  schemaEnum([]string{"evidence_conflict"}),
			"status":                schemaEnum([]string{"open", "resolved"}),
			"preferred_position_id": nullableString("Preferred evidence position when resolved.", 128),
			"positions":             array(recallEvidenceConflictPositionSchema(), 2, 10),
			"positions_truncated":   map[string]any{"type": "boolean"},
		},
	)
}

func recallEvidenceConflictPositionSchema() map[string]any {
	return closedObject(
		[]string{"position_id", "disposition", "evidence_id", "occurrence_id", "quote", "span_start", "span_end", "authority"},
		map[string]any{
			"position_id":   schemaString("Evidence conflict position ID.", 128),
			"disposition":   schemaEnum([]string{"candidate", "preferred"}),
			"evidence_id":   schemaString("Canonical evidence ID.", 128),
			"occurrence_id": schemaString("Exact cited occurrence ID.", 128),
			"quote":         schemaString("Exact server-derived evidence quote.", 4000),
			"span_start":    map[string]any{"type": "integer", "minimum": 0},
			"span_end":      map[string]any{"type": "integer", "minimum": 1},
			"authority":     schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
			"submitted":     map[string]any{"type": "boolean"},
		},
	)
}

func traceEvidenceLifecycleEventSchema() map[string]any {
	return closedObject(
		[]string{"lifecycle_event_id", "lifecycle_operation_id", "target_evidence_id", "action", "created_at"},
		map[string]any{
			"lifecycle_event_id":         schemaString("Append-only evidence lifecycle event ID.", 128),
			"lifecycle_operation_id":     schemaString("Append-only evidence lifecycle operation ID.", 128),
			"target_evidence_id":         schemaString("Retired evidence ID.", 128),
			"owner_profile_id":           schemaString("Lifecycle operation owner.", 128),
			"replacement_evidence_id":    nullableString("Replacement evidence ID when superseded.", 128),
			"replacement_evidence_ids":   stringArraySchema("Complete replacement Evidence set.", 100, 128),
			"replacement_occurrence_ids": stringArraySchema("Ordered exact replacement occurrences.", 100, 128),
			"action":                     schemaEnum([]string{"supersede", "retract"}),
			"reason":                     schemaString("Bounded lifecycle reason.", 1000),
			"created_at":                 map[string]any{"type": "string", "format": "date-time"},
		},
	)
}

func traceEvidenceSchema() map[string]any {
	properties := map[string]any{}
	for _, field := range []string{"evidence_id", "occurrence_id", "ingest_id", "owner_profile_id", "occurrence_owner_profile_id", "source_id", "source_revision_id", "source_key", "source_kind", "revision_token", "current_revision_id", "content_hash", "source_type", "authority", "source_ref"} {
		properties[field] = schemaString("Evidence provenance field.", 1024)
	}
	properties["content"] = schemaString("Exact bounded Evidence content.", memoryEntryMaxLength)
	properties["evidence_index"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 99}
	properties["content_truncated"] = map[string]any{"type": "boolean"}
	properties["content_omitted"] = map[string]any{"type": "boolean"}
	properties["created_at"] = map[string]any{"type": "string", "format": "date-time"}
	properties["labels"] = stringArraySchema("Original Evidence label.", 20, 64)
	properties["metadata"] = boundedMap("Original admitted metadata.")
	properties["session"] = sessionProvenanceSchema()
	properties["partition"] = closedObject([]string{"source_item_id", "submitted_index", "partition_index", "span_start", "span_end", "original_content_hash", "policy_version"}, map[string]any{
		"source_item_id": uuidStringSchema("Immutable original submitted item."), "submitted_index": map[string]any{"type": "integer", "minimum": 0, "maximum": 19}, "partition_index": map[string]any{"type": "integer", "minimum": 0, "maximum": 99}, "span_start": map[string]any{"type": "integer", "minimum": 0, "maximum": memoryEntryMaxLength}, "span_end": map[string]any{"type": "integer", "minimum": 1, "maximum": memoryEntryMaxLength}, "original_content_hash": schemaString("Original exact item hash.", 128), "policy_version": schemaString("Server partition policy version.", 128),
	})
	return closedObject([]string{"evidence_id", "content_hash"}, properties)
}
func traceSourceRevisionSchema() map[string]any {
	properties := map[string]any{}
	for _, field := range []string{"source_id", "source_revision_id", "owner_profile_id", "source_key", "revision_token", "supersedes_revision_id", "current_revision_id", "content_hash"} {
		properties[field] = schemaString("Source revision provenance.", 1024)
	}
	properties["envelope"] = boundedMap("Original bounded source envelope.")
	properties["created_at"] = map[string]any{"type": "string", "format": "date-time"}
	return closedObject([]string{"source_id", "source_revision_id", "owner_profile_id", "source_key", "revision_token", "current_revision_id", "content_hash", "envelope", "created_at"}, properties)
}
func traceEvidenceDecisionSchema() map[string]any {
	return closedObject([]string{"decision_id", "ingest_id", "kind", "decision", "created_at"}, map[string]any{
		"decision_id": schemaString("Evidence decision identity.", 128), "ingest_id": schemaString("Original submission identity.", 128), "occurrence_id": schemaString("Exact occurrence identity.", 128), "kind": schemaString("Decision kind.", 128), "decision": schemaString("Bounded original decision.", 128), "reason": schemaString("Bounded decision reason.", 1000), "created_at": map[string]any{"type": "string", "format": "date-time"},
	})
}
func traceSearchDocumentSchema() map[string]any {
	properties := map[string]any{}
	for _, field := range []string{"search_document_id", "owner_profile_id", "source_kind", "source_id", "embedding_contract_id", "search_state", "document_hash"} {
		properties[field] = schemaString("Evidence search projection metadata.", 128)
	}
	for _, field := range []string{"source_version", "document_version", "embedding_dimensions"} {
		properties[field] = map[string]any{"type": "integer", "minimum": 1}
	}
	for _, field := range []string{"created_at", "updated_at"} {
		properties[field] = map[string]any{"type": "string", "format": "date-time"}
	}
	return closedObject([]string{"search_document_id", "source_kind", "source_id", "source_version", "document_version", "embedding_contract_id", "embedding_dimensions", "search_state", "document_hash"}, properties)
}
