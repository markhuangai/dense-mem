package registry

import "github.com/markhuangai/dense-mem/internal/domain"

func contractInput(required []string, properties map[string]any) map[string]any {
	requireNonEmptyStrings(required, properties)
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func rememberInputSchema() map[string]any {
	return contractInput([]string{"target_space", "evidence", "idempotency_key"}, map[string]any{
		"target_space": schemaEnum([]string{"private", "team_shared"}), "evidence": evidenceArraySchema(),
		"idempotency_key": nonEmptyStringSchema("One retained batch retry key scoped to team and profile.", 128),
	})
}

func evidenceArraySchema() map[string]any {
	content := memoryEntryString("Verbatim evidence text.")
	content["minLength"] = 1
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 20,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"content"},
			"properties": map[string]any{
				"content":                  content,
				"force_insert":             map[string]any{"type": "boolean", "description": "Bypass semantic duplicate reuse; exact reuse still applies."},
				"source_type":              schemaEnum([]string{"conversation", "document", "observation", "manual"}),
				"source":                   schemaString("Bounded provenance label.", 256),
				"authority":                schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
				"source_group":             schemaString("Proposed evidence source grouping.", 256),
				"source_key":               schemaString("Stable source-owner-scoped identity.", 256),
				"source_revision":          schemaString("Opaque current source revision token.", 256),
				"previous_source_revision": schemaString("Exact previous source revision token.", 256),
				"supersedes_evidence_ids":  stringArraySchema("Caller-owned evidence ID to supersede.", 50, 128),
				"labels":                   stringArraySchema("Evidence label.", 20, 64),
				"metadata":                 boundedMap("Source-policy-approved metadata."),
			},
			"additionalProperties": false,
		},
	}
}

func retractEvidenceInputSchema() map[string]any {
	evidenceIDs := stringArraySchema("Caller-owned evidence ID to retract.", 50, 128)
	evidenceIDs["minItems"] = 1
	return contractInput([]string{"evidence_ids", "reason", "idempotency_key"}, map[string]any{
		"evidence_ids":    evidenceIDs,
		"reason":          nonEmptyStringSchema("Required bounded retraction reason.", 1000),
		"idempotency_key": nonEmptyStringSchema("Retraction retry key scoped to team and profile.", 128),
	})
}

func recallMemoryInputSchema() map[string]any {
	topics := array(uuidStringSchema("Current direct Evidence topic ID."), 1, 10)
	topics["uniqueItems"] = true
	return contractInput(nil, map[string]any{
		"query":     schemaString("Natural-language query; optional with topic_ids.", 512),
		"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"topic_ids": topics, "topic_limit": map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "default": 3},
		"valid_at": nullableDateTime("Real-world Evidence time."), "known_at": nullableDateTime("System-knowledge Evidence time."),
		"known_evidence_ids": stringArraySchema("Evidence UUID already seen.", 200, 128),
	})
}

func traceMemoryInputSchema() map[string]any {
	return contractInput([]string{"evidence_id"}, map[string]any{
		"evidence_id": uuidStringSchema("Authorized Evidence ID."), "include_evidence_content": map[string]any{"type": "boolean"},
		"include_transitions":        map[string]any{"type": "boolean"},
		"max_events":                 map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100},
		"max_fragment_content_runes": map[string]any{"type": "integer", "minimum": 1, "maximum": 8000, "default": 2000},
	})
}

func recallFeedbackInputSchema() map[string]any {
	return contractInput([]string{"recalls"}, map[string]any{
		"recalls": map[string]any{
			"type":     "array",
			"minItems": 1,
			"maxItems": 20,
			"items": map[string]any{
				"type":     "object",
				"required": []string{"recall_event_id", "used", "answer_supported", "quality"},
				"properties": map[string]any{
					"recall_event_id":  nonEmptyStringSchema("Recall event ID.", 128),
					"used":             map[string]any{"type": "boolean"},
					"answer_supported": map[string]any{"type": "boolean"},
					"quality":          schemaEnum([]string{"high", "medium", "low"}),
					"missing_context":  map[string]any{"type": "boolean"},
					"irrelevant":       map[string]any{"type": "boolean"},
					"feedback_comment": schemaString("Bounded feedback note.", 1000),
					"irrelevant_result_refs": array(closedObject(
						[]string{"type", "id", "rank"},
						map[string]any{
							"type": schemaEnum([]string{"evidence", "topic"}),
							"id":   schemaString("Result ID from the recall event.", 128),
							"rank": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
						},
					), 0, 100),
				},
				"additionalProperties": false,
			},
		},
	})
}

func exportMemoryPackInputSchema() map[string]any {
	ids := array(uuidStringSchema("Authorized Evidence ID to export."), 1, 500)
	ids["uniqueItems"] = true
	return contractInput([]string{"name", "evidence_ids"}, map[string]any{
		"name": schemaString("Memory pack name.", 256), "description": schemaString("Memory pack description.", 1024), "evidence_ids": ids, "include_evidence": map[string]any{"type": "boolean"},
	})
}

func rememberOutputSchema() map[string]any {
	properties := map[string]any{
		"space_kind":       schemaEnum([]string{"private", "team_shared"}),
		"contract_version": schemaEnum(domain.AcceptedContractVersions()),
		"submission_id":    schemaString("Submission ID.", 128),
		"submission_kind":  schemaEnum([]string{"remember"}),
		"processing_state": schemaEnum([]string{"completed", "failed"}),
		"search_state":     schemaEnum([]string{"current", "not_required"}),
		"correlation_id":   schemaString("Request correlation ID.", 128),
		"evidence":         array(rememberEvidenceStatusSchema(), 0, 100),
		"errors":           submissionStatusErrorArraySchema(),
		"warnings":         stringArraySchema("Bounded server diagnostics about optional assessor context.", 20, 512),
	}
	return closedObject(
		[]string{"contract_version", "space_kind", "submission_id", "submission_kind", "processing_state", "search_state", "correlation_id", "evidence", "errors"},
		properties,
	)
}

func rememberEvidenceStatusSchema() map[string]any {
	return closedObject(
		[]string{"disposition", "evidence_index", "superseded_evidence_ids", "search_state"},
		map[string]any{
			"disposition":             schemaEnum([]string{"stored", "not_stored"}),
			"evidence_id":             schemaString("Durable evidence ID when stored.", 128),
			"content_hash":            schemaString("Evidence content hash.", 128),
			"evidence_index":          map[string]any{"type": "integer", "minimum": 0},
			"superseded_evidence_ids": stringArraySchema("Evidence ID superseded by this evidence.", 50, 128),
			"search_state":            schemaEnum([]string{"current", "not_required"}),
			"reason":                  schemaString("Bounded reason when not stored.", 256),
			"error":                   map[string]any{"oneOf": []any{map[string]any{"type": "null"}, submissionStatusErrorSchema()}},
		},
	)
}

func retractEvidenceOutputSchema() map[string]any {
	return closedObject(
		[]string{
			"decision_id",
			"processing_state",
			"retracted_evidence_ids",
		},
		map[string]any{
			"decision_id":            schemaString("Append-only lifecycle decision ID.", 128),
			"processing_state":       schemaEnum([]string{"completed"}),
			"retracted_evidence_ids": stringArraySchema("Retracted evidence ID.", 50, 128),
		},
	)
}

func recallMemoryOutputSchema() map[string]any {
	return closedObject(
		[]string{
			"recall_id", "results", "conflicts", "related_topics", "topics_truncated", "search_states", "degradations", "suggested_actions",
		},
		map[string]any{
			"recall_id": schemaString("Recall event ID.", 128),
			"results":   array(recallResultSchema(), 0, 50),
			"conflicts": array(recallConflictSchema(), 0, 20),

			"related_topics": array(relatedTopicSchema(), 0, 10), "topics_truncated": map[string]any{"type": "boolean"},
			"search_states":     recallSearchStatesSchema(),
			"degradations":      array(recallDegradationSchema(), 0, 10),
			"suggested_actions": array(recallSuggestedActionSchema(), 0, 2),
		},
	)
}

func recallSuggestedActionSchema() map[string]any {
	return closedObject(
		[]string{"tool", "guidance"},
		map[string]any{
			"tool":            schemaEnum([]string{ToolSubmitRecallSessionFeedback}),
			"guidance":        schemaString("Bounded next-step guidance.", 512),
			"recall_event_id": schemaString("Recall event ID accepted by the feedback tool.", 128),
		},
	)
}

func recallSearchStatesSchema() map[string]any {
	return closedObject(
		[]string{"evidence"},
		map[string]any{
			"evidence": schemaEnum(domain.SearchProjectionStates()),
		},
	)
}

func recallDegradationSchema() map[string]any {
	return closedObject(
		[]string{"frontier", "code", "message"},
		map[string]any{
			"frontier":         schemaEnum([]string{"evidence", "topics", "feedback"}),
			"required_failure": map[string]any{"type": "boolean"},
			"optional":         map[string]any{"type": "boolean"},
			"code":             schemaString("Stable bounded degradation code.", 128),
			"message":          schemaString("Safe bounded degradation message.", 512),
		},
	)
}

func recallResultSchema() map[string]any {
	return closedObject(
		[]string{"evidence_id", "context", "space_kind", "equivalent_evidence_ids", "equivalents_truncated"},
		map[string]any{
			"evidence_id":             schemaString("Evidence ID.", 128),
			"equivalent_evidence_ids": stringArraySchema("Authorized alternate evidence source ID with equivalent complete content.", 20, 128),
			"equivalents_truncated":   map[string]any{"type": "boolean"},
			"context":                 schemaString("Bounded evidence context.", 2000),
			"space_kind":              schemaEnum([]string{"team_shared", "private"}),
		},
	)
}

func traceMemoryOutputSchema() map[string]any {
	return closedObject([]string{"evidence", "occurrences", "source_revisions", "decisions", "lifecycle", "search_documents", "omissions", "truncated"}, map[string]any{
		"evidence": traceEvidenceSchema(), "occurrences": array(traceEvidenceSchema(), 0, 500), "source_revisions": array(traceSourceRevisionSchema(), 0, 500),
		"decisions": array(traceEvidenceDecisionSchema(), 0, 500), "lifecycle": array(traceEvidenceLifecycleEventSchema(), 0, 500),
		"search_documents": array(traceSearchDocumentSchema(), 0, 500), "omissions": stringArraySchema("Explicit omission reason.", 500, 128), "truncated": map[string]any{"type": "boolean"},
	})
}

func stringArraySchema(description string, maxItems int, maxLen int) map[string]any {
	item := schemaString(description, maxLen)
	item["minLength"] = 1
	schema := map[string]any{
		"type":        "array",
		"items":       item,
		"uniqueItems": true,
	}
	if maxItems > 0 {
		schema["maxItems"] = maxItems
	}
	return schema
}

func relatedTopicSchema() map[string]any {
	return closedObject([]string{"topic_id", "name", "description", "eligible_evidence_count", "rank"}, map[string]any{
		"topic_id": uuidStringSchema("Direct Evidence topic identity."), "name": schemaString("Current topic name.", 128), "description": schemaString("Current topic description.", 1000),
		"eligible_evidence_count": map[string]any{"type": "integer", "minimum": 0}, "rank": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
	})
}
