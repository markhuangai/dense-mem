package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func recallFeedbackOutputSchema() map[string]any {
	return closedObject(
		[]string{"recorded", "recorded_count"},
		map[string]any{
			"recorded":        map[string]any{"type": "boolean"},
			"recorded_count":  map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
			"partial_success": map[string]any{"type": "boolean"},
			"failed_index":    map[string]any{"type": "integer", "minimum": 0, "maximum": 19},
			"error":           schemaString("Bounded feedback failure summary.", 128),
		},
	)
}

func listDreamsOutputSchema() map[string]any {
	return closedObject(
		[]string{"dreams"},
		map[string]any{
			"dreams":      array(hypothesisSummarySchema(), 0, 100),
			"next_cursor": nullableString("Opaque next-page cursor.", 512),
		},
	)
}

func getDreamOutputSchema() map[string]any {
	return closedObject(
		[]string{"hypothesis"},
		map[string]any{"hypothesis": hypothesisSchema()},
	)
}

func resolveDreamFeedbackOutputSchema() map[string]any {
	return closedObject(
		[]string{"hypothesis_id", "status"},
		map[string]any{
			"hypothesis_id": schemaString("Hypothesis ID.", 128),
			"status":        schemaEnum(domain.HypothesisStatuses()),
			"ingest_id":     schemaString("Placement run ID for submitted evidence.", 128),
		},
	)
}

func findMemoryPackCandidatesOutputSchema() map[string]any {
	return closedObject(
		[]string{"candidates"},
		map[string]any{
			"candidates": array(memoryPackCandidateSchema(), 0, 100),
		},
	)
}

func memoryPackCandidateSchema() map[string]any {
	return closedObject(
		[]string{"relationship_id", "subject", "predicate", "object", "polarity", "rank"},
		map[string]any{
			"relationship_id": schemaString("Exportable Relationship ID.", 128),
			"subject":         entityHandleSchema(),
			"predicate":       schemaString("Registered predicate key.", 128),
			"object":          semanticObjectSchema(),
			"polarity":        schemaEnum([]string{"+", "-"}),
			"rank":            map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		},
	)
}

func exportMemoryPackOutputSchema() map[string]any {
	return closedObject(
		[]string{"artifact_json", "content_sha256", "filename", "counts", "omissions"},
		map[string]any{
			"artifact_json":  schemaString("Canonical dense-mem.memory-pack.v2.4 JSON.", memoryPackArtifactMaxLength),
			"content_sha256": sHA256Schema(),
			"filename":       schemaString("Suggested artifact filename.", 256),
			"counts":         countMapSchema(),
			"omissions":      array(memoryPackOmissionSchema(), 0, 500),
			"signature":      memoryPackSignatureSchema(),
		},
	)
}

func inspectMemoryPackOutputSchema() map[string]any {
	return closedObject(
		[]string{"valid", "format", "content_sha256", "mode", "counts", "conflicts", "expected_outcomes"},
		map[string]any{
			"valid": map[string]any{"type": "boolean"},
			"format": schemaEnum([]string{
				skillpackservice.MemoryPackFormat,
				"dense-mem.memory-pack.v2.3",
				skillpackservice.SchemaVersion,
				skillpackservice.LegacySchemaVersion,
			}),
			"content_sha256":    sHA256Schema(),
			"mode":              schemaEnum([]string{"review", "trusted"}),
			"counts":            countMapSchema(),
			"conflicts":         array(memoryPackConflictSchema(), 0, 500),
			"expected_outcomes": countMapSchema(),
			"signature":         memoryPackSignatureSchema(),
		},
	)
}

func importMemoryPackOutputSchema() map[string]any {
	return closedObject(
		[]string{"import_id", "processing_state", "ingest_ids", "omissions"},
		map[string]any{
			"import_id":        schemaString("Memory-pack import ledger ID.", 128),
			"processing_state": schemaEnum([]string{"queued", "processing", "completed", "failed"}),
			"ingest_ids":       stringArraySchema("Placement run ID staged by import.", 500, 128),
			"omissions":        array(memoryPackOmissionSchema(), 0, 500),
		},
	)
}

func rollbackMemoryPackImportOutputSchema() map[string]any {
	return closedObject(
		[]string{"import_id", "dry_run", "safe", "blockers", "affected_relationship_ids"},
		map[string]any{
			"import_id":                 schemaString("Memory-pack import ledger ID.", 128),
			"dry_run":                   map[string]any{"type": "boolean"},
			"safe":                      map[string]any{"type": "boolean"},
			"blockers":                  array(rollbackBlockerSchema(), 0, 500),
			"affected_relationship_ids": stringArraySchema("Caller-owned Relationship affected by rollback.", 500, 128),
			"impact_token":              schemaString("Version-bound rollback apply token.", 256),
			"applied":                   map[string]any{"type": "boolean"},
		},
	)
}

func memoryPackOmissionSchema() map[string]any {
	return closedObject(
		[]string{"item_id", "reason"},
		map[string]any{
			"item_id": schemaString("Artifact-local item ID.", 128),
			"reason":  schemaString("Bounded omission reason.", 1000),
		},
	)
}

func memoryPackConflictSchema() map[string]any {
	return closedObject(
		[]string{"item_id", "kind", "allowed_decisions"},
		map[string]any{
			"item_id": schemaString("Artifact-local item ID.", 128),
			"kind":    schemaString("Conflict kind.", 128),
			"allowed_decisions": stringArraySchema(
				"Allowed conflict decision.", 5, 64,
			),
		},
	)
}

func memoryPackSignatureSchema() map[string]any {
	return closedObject(
		nil,
		map[string]any{
			"algorithm": schemaString("Signature algorithm.", 64),
			"key_id":    schemaString("Configured signer key ID.", 128),
			"signature": schemaString("Encoded signature.", 2048),
			"verified":  map[string]any{"type": "boolean"},
		},
	)
}

func rollbackBlockerSchema() map[string]any {
	return closedObject(
		[]string{"code", "message"},
		map[string]any{
			"code":            schemaString("Typed rollback blocker code.", 128),
			"message":         schemaString("Bounded rollback blocker message.", 1000),
			"relationship_id": schemaString("Blocked Relationship ID.", 128),
		},
	)
}

func countMapSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "integer", "minimum": 0},
		"maxProperties":        32,
		"x-bounded-map":        true,
	}
}

func sHA256Schema() map[string]any {
	return map[string]any{
		"type":      "string",
		"minLength": 64,
		"maxLength": 64,
		"pattern":   "^[a-f0-9]{64}$",
	}
}
