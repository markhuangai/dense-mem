package registry

import "github.com/markhuangai/dense-mem/internal/domain"

func sessionInputSchema() map[string]any {
	event := closedObject([]string{"event_id", "text"}, map[string]any{
		"event_id":    schemaString("Immutable framework event identifier.", 256),
		"text":        schemaString("Exact user-authored text. Dense-Mem owns chunking; send the original event.", 0),
		"occurred_at": map[string]any{"type": "string", "format": "date-time", "maxLength": 128},
	})
	schema := closedObject([]string{"idempotency_key", "framework", "app_name", "user_id", "session_id", "events"}, map[string]any{
		"idempotency_key": schemaString("Retain this key and the complete original request for unchanged-request retries.", 128),
		"framework":       schemaString("Framework provenance; never authorization.", 256),
		"app_name":        schemaString("Application provenance; never authorization.", 256),
		"user_id":         schemaString("External user provenance; never profile identity.", 256),
		"session_id":      schemaString("External session provenance; never a memory-space selector.", 256),
		"events":          array(event, 1, 20),
	})
	schema["x-processing-limits"] = map[string]any{"max_windows": 8, "source_tokens_per_window": 8192, "max_cited_excerpts": 100, "processing_seconds": 180}
	return schema
}

func sessionOutputSchema() map[string]any {
	return closedObject([]string{"contract_version", "submission_id", "submission_kind", "correlation_id", "accepted_event_count", "duplicate_event_count", "processing_state", "search_state", "events", "relationship_results", "errors"}, map[string]any{
		"contract_version":      schemaEnum([]string{domain.ContractVersion}),
		"submission_id":         schemaString("Durable intake submission identifier.", 128),
		"submission_kind":       schemaEnum([]string{"session_ingest"}),
		"correlation_id":        schemaString("Invocation correlation identifier.", 128),
		"accepted_event_count":  map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
		"duplicate_event_count": map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
		"processing_state":      schemaEnum([]string{"completed", "failed"}),
		"search_state":          schemaEnum([]string{"current", "not_required"}),
		"events": array(closedObject([]string{"event_id", "disposition", "processing_state", "evidence_ids"}, map[string]any{
			"event_id":         schemaString("Original immutable event identifier.", 256),
			"disposition":      schemaEnum([]string{"accepted", "duplicate"}),
			"processing_state": schemaEnum([]string{"completed", "failed"}),
			"evidence_ids":     stringArraySchema("Authorized canonical evidence ID derived from this event.", 100, 128),
		}), 1, 20),
		"relationship_results": submissionRelationshipResultsSchema(),
		"errors": array(closedObject([]string{"code", "message", "retryable", "next_action", "remediation"}, map[string]any{
			"code": schemaEnum(contractErrorCodes()), "message": schemaString("Bounded processing failure.", 512),
			"retryable":   map[string]any{"type": "boolean"},
			"next_action": schemaEnum([]string{"retry_same_request", "contact_operator", "none"}),
			"remediation": schemaString("Recovery preserves the complete original request and retained key.", 512),
		}), 0, 20),
		"warnings": stringArraySchema("Optional bounded context omissions.", 20, 512),
	})
}

func sessionProvenanceSchema() map[string]any {
	return closedObject([]string{"framework", "app_name", "user_id", "session_id", "event_id", "event_index", "span_start", "span_end"}, map[string]any{
		"framework":   schemaString("Original framework provenance.", 256),
		"app_name":    schemaString("Original application provenance.", 256),
		"user_id":     schemaString("Original external user provenance.", 256),
		"session_id":  schemaString("Original external session provenance.", 256),
		"event_id":    schemaString("Immutable original event identifier.", 256),
		"event_index": map[string]any{"type": "integer", "minimum": 0, "maximum": 19},
		"span_start":  map[string]any{"type": "integer", "minimum": 0},
		"span_end":    map[string]any{"type": "integer", "minimum": 1},
		"occurred_at": map[string]any{"type": "string", "format": "date-time", "maxLength": 128},
	})
}
