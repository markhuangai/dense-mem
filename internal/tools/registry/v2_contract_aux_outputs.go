package registry

import "github.com/markhuangai/dense-mem/internal/domain"

func v2RecallFeedbackOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"recorded", "recorded_count"},
		map[string]any{
			"recorded":       map[string]any{"type": "boolean"},
			"recorded_count": map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
		},
	)
}

func v2ListDreamsOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"dreams"},
		map[string]any{
			"dreams":      v2Array(v2HypothesisSummarySchema(), 0, 100),
			"next_cursor": v2NullableString("Opaque next-page cursor.", 512),
		},
	)
}

func v2GetDreamOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"hypothesis"},
		map[string]any{"hypothesis": v2HypothesisSchema()},
	)
}

func v2ResolveDreamFeedbackOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"hypothesis_id", "status"},
		map[string]any{
			"hypothesis_id": schemaString("Hypothesis ID.", 128),
			"status":        schemaEnum(domain.V2HypothesisStatuses()),
			"ingest_id":     schemaString("Placement run ID for submitted evidence.", 128),
		},
	)
}

func v2ListCommunitiesOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"communities"},
		map[string]any{
			"communities": v2Array(v2CommunitySummarySchema(), 0, 100),
			"next_cursor": v2NullableString("Opaque next-page cursor.", 512),
		},
	)
}

func v2CommunitySummarySchema() map[string]any {
	return v2ClosedObject(
		[]string{"community_id", "run_id", "summary", "status", "created_at", "members", "sources"},
		map[string]any{
			"community_id":              schemaString("Community snapshot ID.", 128),
			"run_id":                    schemaString("Community generation run ID.", 128),
			"summary":                   schemaString("Bounded community summary.", 2000),
			"summary_generator_kind":    schemaEnum([]string{"deterministic", "provider"}),
			"summary_generator_version": schemaString("Summary generator version.", 128),
			"status":                    schemaEnum([]string{"current", "superseded"}),
			"created_at":                map[string]any{"type": "string", "format": "date-time"},
			"superseded_at":             v2NullableDateTime("Community supersession time."),
			"members":                   v2Array(v2CommunityMemberSchema(), 0, 500),
			"sources":                   v2Array(v2CommunitySourceSchema(), 0, 1000),
		},
	)
}

func v2CommunityMemberSchema() map[string]any {
	return v2ClosedObject(
		[]string{"entity_id", "score", "rank"},
		map[string]any{
			"entity_id": schemaString("Community member Entity ID.", 128),
			"score":     map[string]any{"type": "number", "minimum": 0},
			"rank":      map[string]any{"type": "integer", "minimum": 1},
		},
	)
}

func v2CommunitySourceSchema() map[string]any {
	return v2ClosedObject(
		[]string{"relationship_id", "owner_profile_id", "relationship_version"},
		map[string]any{
			"relationship_id":      schemaString("Source Relationship ID.", 128),
			"owner_profile_id":     schemaString("Source Relationship owner profile ID.", 128),
			"relationship_version": map[string]any{"type": "integer", "minimum": 1},
		},
	)
}

func v2FindMemoryPackCandidatesOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"candidates"},
		map[string]any{
			"candidates": v2Array(v2MemoryPackCandidateSchema(), 0, 100),
		},
	)
}

func v2MemoryPackCandidateSchema() map[string]any {
	return v2ClosedObject(
		[]string{"relationship_id", "subject", "predicate", "object", "polarity", "rank"},
		map[string]any{
			"relationship_id": schemaString("Exportable Relationship ID.", 128),
			"subject":         v2EntityHandleSchema(),
			"predicate":       schemaString("Registered predicate key.", 128),
			"object":          v2SemanticObjectSchema(),
			"polarity":        schemaEnum([]string{"+", "-"}),
			"rank":            map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		},
	)
}

func v2ExportMemoryPackOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"artifact_json", "content_sha256", "filename", "counts", "omissions"},
		map[string]any{
			"artifact_json":  schemaString("Canonical dense-mem.memory-pack.v2 JSON.", v2MemoryPackArtifactMaxLength),
			"content_sha256": v2SHA256Schema(),
			"filename":       schemaString("Suggested artifact filename.", 256),
			"counts":         v2CountMapSchema(),
			"omissions":      v2Array(v2MemoryPackOmissionSchema(), 0, 500),
			"signature":      v2MemoryPackSignatureSchema(),
		},
	)
}

func v2InspectMemoryPackOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"valid", "format", "content_sha256", "mode", "counts", "conflicts", "expected_outcomes"},
		map[string]any{
			"valid":             map[string]any{"type": "boolean"},
			"format":            schemaEnum([]string{"dense-mem.memory-pack.v2"}),
			"content_sha256":    v2SHA256Schema(),
			"mode":              schemaEnum([]string{"review", "trusted"}),
			"counts":            v2CountMapSchema(),
			"conflicts":         v2Array(v2MemoryPackConflictSchema(), 0, 500),
			"expected_outcomes": v2CountMapSchema(),
			"signature":         v2MemoryPackSignatureSchema(),
		},
	)
}

func v2ImportMemoryPackOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"import_id", "processing_state", "ingest_ids", "omissions"},
		map[string]any{
			"import_id":        schemaString("Memory-pack import ledger ID.", 128),
			"processing_state": schemaEnum([]string{"queued", "processing", "completed", "failed"}),
			"ingest_ids":       v2StringArraySchema("Placement run ID staged by import.", 500, 128),
			"omissions":        v2Array(v2MemoryPackOmissionSchema(), 0, 500),
		},
	)
}

func v2RollbackMemoryPackImportOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"import_id", "dry_run", "safe", "blockers", "affected_relationship_ids"},
		map[string]any{
			"import_id":                 schemaString("Memory-pack import ledger ID.", 128),
			"dry_run":                   map[string]any{"type": "boolean"},
			"safe":                      map[string]any{"type": "boolean"},
			"blockers":                  v2Array(v2RollbackBlockerSchema(), 0, 500),
			"affected_relationship_ids": v2StringArraySchema("Caller-owned Relationship affected by rollback.", 500, 128),
			"impact_token":              schemaString("Version-bound rollback apply token.", 256),
			"applied":                   map[string]any{"type": "boolean"},
		},
	)
}

func v2MemoryPackOmissionSchema() map[string]any {
	return v2ClosedObject(
		[]string{"item_id", "reason"},
		map[string]any{
			"item_id": schemaString("Artifact-local item ID.", 128),
			"reason":  schemaString("Bounded omission reason.", 1000),
		},
	)
}

func v2MemoryPackConflictSchema() map[string]any {
	return v2ClosedObject(
		[]string{"item_id", "kind", "allowed_decisions"},
		map[string]any{
			"item_id": schemaString("Artifact-local item ID.", 128),
			"kind":    schemaString("Conflict kind.", 128),
			"allowed_decisions": v2StringArraySchema(
				"Allowed conflict decision.", 5, 64,
			),
		},
	)
}

func v2MemoryPackSignatureSchema() map[string]any {
	return v2ClosedObject(
		nil,
		map[string]any{
			"algorithm": schemaString("Signature algorithm.", 64),
			"key_id":    schemaString("Configured signer key ID.", 128),
			"signature": schemaString("Encoded signature.", 2048),
			"verified":  map[string]any{"type": "boolean"},
		},
	)
}

func v2RollbackBlockerSchema() map[string]any {
	return v2ClosedObject(
		[]string{"code", "message"},
		map[string]any{
			"code":            schemaString("Typed rollback blocker code.", 128),
			"message":         schemaString("Bounded rollback blocker message.", 1000),
			"relationship_id": schemaString("Blocked Relationship ID.", 128),
		},
	)
}

func v2CountMapSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "integer", "minimum": 0},
		"maxProperties":        32,
		"x-bounded-map":        true,
	}
}

func v2SHA256Schema() map[string]any {
	return map[string]any{
		"type":      "string",
		"minLength": 64,
		"maxLength": 64,
		"pattern":   "^[a-f0-9]{64}$",
	}
}
