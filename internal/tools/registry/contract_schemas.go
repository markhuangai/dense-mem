package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
)

func contractInput(required []string, properties map[string]any) map[string]any {
	requireNonEmptyStrings(required, properties)
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
		"x-contract-version":   domain.ContractVersion,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func rememberInputSchema() map[string]any {
	return contractInput([]string{"evidence", "relationships"}, map[string]any{
		"evidence":               evidenceArraySchema(),
		"relationships":          relationshipSubmissionArraySchema(),
		"idempotency_key":        schemaString("Ingest retry key scoped to team and profile.", 128),
		"replaces_submission_id": uuidStringSchema("Same-owner held submission to replace."),
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
				"source_type":              schemaEnum([]string{"conversation", "document", "observation", "manual"}),
				"source":                   schemaString("Bounded provenance label.", 256),
				"authority":                schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
				"source_group":             schemaString("Proposed evidence source grouping.", 256),
				"source_key":               schemaString("Stable source-owner-scoped identity.", 256),
				"source_revision":          schemaString("Opaque current source revision token.", 256),
				"previous_source_revision": schemaString("Exact previous source revision token.", 256),
				"supersedes_evidence_ids":  stringArraySchema("Caller-owned evidence ID to supersede.", 50, 128),
				"idempotency_key":          schemaString("Evidence retry key scoped to team and profile.", 128),
				"labels":                   stringArraySchema("Evidence label.", 20, 64),
				"metadata":                 boundedMap("Source-policy-approved metadata."),
			},
			"additionalProperties": false,
		},
	}
}

func relationshipSubmissionArraySchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"minItems":    1,
		"maxItems":    200,
		"description": "Relationship support entries across the submission must collectively cover every submitted evidence item. Spans use Unicode code-point offsets.",
		"items":       relationshipSubmissionSchema(),
	}
}

func relationshipSubmissionSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"ref", "subject", "predicate", "object", "polarity", "modality", "supports"},
		"properties": map[string]any{
			"ref":               nonEmptyStringSchema("Client-local Relationship proposal ref.", 128),
			"subject":           inlineRelationshipEntitySchema("Evidence-supported subject Entity."),
			"predicate":         relationshipPredicateSchema(),
			"object":            relationshipObjectSchema(),
			"polarity":          schemaEnum([]string{"+", "-"}),
			"modality":          schemaEnum([]string{"statement", "question", "proposal", "speculation", "quoted"}),
			"valid_from":        nullableDateTime("Evidence-supported validity start."),
			"valid_to":          nullableDateTime("Evidence-supported validity end."),
			"correction_target": relationshipCorrectionTargetSchema(),
			"conflict_context":  relationshipConflictContextSchema(),
			"client_comment":    nullableString("Non-authoritative extraction note.", 1000),
			"supports":          relationshipSupportArraySchema(),
		},
		"additionalProperties": false,
	}
}

func inlineRelationshipEntitySchema(description string) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name", "entity_kind", "span"},
		"properties": map[string]any{
			"name":            nonEmptyStringSchema(description, 256),
			"entity_kind":     schemaEnum(domain.EntityKinds()),
			"known_entity_id": nullableString("Server-issued Entity candidate hint.", 128),
			"span":            relationshipSpanSchema(),
		},
		"additionalProperties": false,
	}
}

func relationshipPredicateSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"proposed_key", "surface", "span"},
		"properties": map[string]any{
			"proposed_key": nonEmptyStringSchema("Best-effort team predicate key proposal.", 128),
			"surface":      nonEmptyStringSchema("Exact predicate surface from submitted evidence.", 256),
			"span":         relationshipSpanSchema(),
		},
		"additionalProperties": false,
	}
}

func relationshipObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{
				"required": []string{"entity"},
				"not":      map[string]any{"required": []string{"value"}},
			},
			map[string]any{
				"required": []string{"value"},
				"not":      map[string]any{"required": []string{"entity"}},
			},
		},
		"properties": map[string]any{
			"entity": inlineRelationshipEntitySchema("Evidence-supported object Entity."),
			"value":  relationshipValueSchema(),
		},
		"additionalProperties": false,
	}
}

func relationshipValueSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"type", "value", "surface", "span"},
		"properties": map[string]any{
			"type":    schemaEnum(domain.ValueTypes()),
			"value":   map[string]any{"type": []any{"string", "number", "boolean"}},
			"surface": nonEmptyStringSchema("Exact typed Value surface from submitted evidence.", 1024),
			"span":    relationshipSpanSchema(),
			"display": schemaString("Optional display form from submitted evidence.", 1024),
			"unit":    schemaString("Optional unit from submitted evidence.", 128),
		},
		"additionalProperties": false,
	}
}

func relationshipSupportArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 20,
		"items":    relationshipSpanSchema(),
	}
}

func relationshipSpanSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []string{"evidence_index", "start", "end"},
		"additionalProperties": false,
		"properties": map[string]any{
			"evidence_index": map[string]any{"type": "integer", "minimum": 0},
			"start":          map[string]any{"type": "integer", "minimum": 0},
			"end":            map[string]any{"type": "integer", "minimum": 0},
		},
	}
}

func relationshipCorrectionTargetSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"relationship_id", "expected_version"},
		"properties": map[string]any{
			"relationship_id":  schemaString("Same-team Relationship target.", 128),
			"expected_version": map[string]any{"type": "integer", "minimum": 1},
		},
	}
}

func relationshipConflictContextSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"conflict_id", "expected_version"},
		"properties": map[string]any{
			"conflict_id":      schemaString("Same-team open Conflict case.", 128),
			"expected_version": map[string]any{"type": "integer", "minimum": 1},
		},
	}
}

func getSubmissionStatusInputSchema() map[string]any {
	return contractInput([]string{"submission_id"}, map[string]any{
		"submission_id": schemaString("Submission ID returned by remember or correct_relationship.", 128),
	})
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

func correctRelationshipInputSchema() map[string]any {
	return contractInput([]string{"action", "idempotency_key"}, map[string]any{
		"action":             schemaEnum([]string{"submit", "confirm"}),
		"relationship_id":    schemaString("Caller-owned Relationship to replace.", 128),
		"expected_version":   map[string]any{"type": "integer", "minimum": 1},
		"patch":              relationshipCorrectionPatchSchema(),
		"supports":           relationshipCorrectionSupportArraySchema(),
		"reason":             nonEmptyStringSchema("Bounded correction reason.", 1000),
		"submission_id":      schemaString("Correction submission awaiting confirmation.", 128),
		"confirmation_token": schemaString("One-round confirmation token.", 256),
		"selection":          relationshipCorrectionSelectionSchema(),
		"idempotency_key":    nonEmptyStringSchema("Retry key scoped to team and profile.", 128),
	})
}

func relationshipCorrectionPatchSchema() map[string]any {
	return closedObject(nil, map[string]any{
		"subject_entity": relationshipCorrectionEntitySchema(),
		"predicate": closedObject([]string{"key"}, map[string]any{
			"key": nonEmptyStringSchema("Registered team predicate key.", 128),
		}),
		"object_entity": relationshipCorrectionEntitySchema(),
	})
}

func relationshipCorrectionEntitySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"required": []string{"entity_id"}, "not": map[string]any{"required": []string{"name"}}},
			map[string]any{"required": []string{"name", "entity_kind"}, "not": map[string]any{"required": []string{"entity_id"}}},
		},
		"properties": map[string]any{
			"entity_id":   schemaString("Existing same-team Entity ID.", 128),
			"name":        nonEmptyStringSchema("Canonical name for deterministic resolution or creation.", 256),
			"entity_kind": schemaEnum(domain.EntityKinds()),
		},
		"additionalProperties": false,
	}
}

func relationshipCorrectionSupportArraySchema() map[string]any {
	return array(closedObject(
		[]string{"evidence_id", "start", "end"},
		map[string]any{
			"evidence_id": schemaString("Existing supporting evidence fragment ID.", 128),
			"start":       map[string]any{"type": "integer", "minimum": 0},
			"end":         map[string]any{"type": "integer", "minimum": 1},
		},
	), 1, 200)
}

func relationshipCorrectionSelectionSchema() map[string]any {
	return closedObject(nil, map[string]any{
		"subject_entity_id": schemaString("Selected subject Entity candidate.", 128),
		"object_entity_id":  schemaString("Selected object Entity candidate.", 128),
	})
}

func recallMemoryInputSchema() map[string]any {
	query := schemaString("Natural-language recall query.", 512)
	query["minLength"] = 1
	return contractInput([]string{"query"}, map[string]any{
		"query":                  query,
		"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"relationship_limit":     map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
		"community_limit":        map[string]any{"type": "integer", "minimum": 0, "maximum": 10},
		"valid_at":               nullableDateTime("Real-world recall time."),
		"known_at":               nullableDateTime("System-knowledge recall time."),
		"known_evidence_ids":     stringArraySchema("Evidence UUID already seen.", 200, 128),
		"known_relationship_ids": stringArraySchema("Relationship UUID already seen.", 200, 128),
		"expand_from_entity_ids": stringArraySchema("Entity UUID for focused expansion.", 50, 128),
	})
}

func traceMemoryInputSchema() map[string]any {
	return contractInput([]string{"relationship_id"}, map[string]any{
		"relationship_id":          schemaString("Same-team Relationship ID.", 128),
		"include_evidence_content": map[string]any{"type": "boolean"},
		"include_verification":     map[string]any{"type": "boolean"},
		"include_transitions":      map[string]any{"type": "boolean"},
		"max_depth":                map[string]any{"type": "integer", "minimum": 0, "maximum": 4},
		"max_edges":                map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"predicate_keys":           stringArraySchema("Registered predicate key filter.", 30, 128),
		"topic":                    nullableString("Optional graph-context topic.", 256),
		"min_relevance":            nullableNumber("Optional graph-context relevance threshold.", 0, 1),
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
							"type": schemaEnum([]string{"relationship", "fragment", "community", "hypothesis"}),
							"id":   schemaString("Result ID from the recall event.", 128),
							"rank": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
						},
					), 0, 100),
					"hypothesis_feedback": array(closedObject(
						[]string{"hypothesis_id", "used", "quality", "contradicted"},
						map[string]any{
							"hypothesis_id":    schemaString("Hypothesis ID returned with recall.", 128),
							"used":             map[string]any{"type": "boolean"},
							"quality":          schemaEnum([]string{"high", "medium", "low"}),
							"contradicted":     map[string]any{"type": "boolean"},
							"feedback_comment": schemaString("Bounded Hypothesis feedback note.", 1000),
						},
					), 0, 100),
				},
				"additionalProperties": false,
			},
		},
	})
}

func listDreamsInputSchema() map[string]any {
	return contractInput(nil, map[string]any{
		"status": schemaEnum(domain.HypothesisStatuses()),
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"cursor": schemaString("Opaque page cursor.", 512),
	})
}

func getDreamInputSchema() map[string]any {
	return contractInput([]string{"hypothesis_id"}, map[string]any{
		"hypothesis_id": schemaString("Hypothesis ID.", 128),
	})
}

func resolveDreamFeedbackInputSchema() map[string]any {
	return contractInput([]string{"hypothesis_id", "decision"}, map[string]any{
		"hypothesis_id": schemaString("Hypothesis ID.", 128),
		"decision": schemaEnum([]string{
			"reinforce",
			"stale",
			"reject",
			"confirm_true",
			"confirm_false",
		}),
		"reason":        schemaString("Bounded lifecycle feedback reason.", 1000),
		"evidence":      evidenceArraySchema(),
		"relationships": relationshipSubmissionArraySchema(),
	})
}

func exportMemoryPackInputSchema() map[string]any {
	return contractInput([]string{"name", "relationship_ids"}, map[string]any{
		"name":                 schemaString("Memory pack name.", 256),
		"description":          schemaString("Memory pack description.", 1024),
		"relationship_ids":     stringArraySchema("Relationship ID to export.", 500, 128),
		"include_evidence":     map[string]any{"type": "boolean"},
		"include_entity_names": map[string]any{"type": "boolean"},
	})
}

func rememberOutputSchema() map[string]any {
	return closedObject(
		[]string{"submission_id", "submission_kind", "processing_state", "check_after_seconds", "status_tool", "correlation_id"},
		map[string]any{
			"submission_id":       schemaString("Submission ID.", 128),
			"submission_kind":     schemaEnum([]string{"remember"}),
			"processing_state":    schemaEnum([]string{"queued", "processing", "completed", "rejected", "quarantined", "failed"}),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
			"status_tool":         schemaEnum([]string{ToolGetSubmissionStatus}),
			"correlation_id":      schemaString("Request correlation ID.", 128),
		},
	)
}

func submissionStatusOutputSchema() map[string]any {
	return closedObject(
		[]string{"submission_id", "submission_kind", "processing_state", "search_state", "check_after_seconds", "evidence", "errors"},
		map[string]any{
			"submission_id":       schemaString("Submission ID.", 128),
			"submission_kind":     schemaEnum([]string{"remember", "relationship_correction"}),
			"processing_state":    schemaEnum([]string{"queued", "processing", "awaiting_confirmation", "completed", "rejected", "quarantined", "failed"}),
			"search_state":        schemaEnum(domain.SearchProjectionStates()),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
			"evidence": array(closedObject(
				[]string{"evidence_id", "evidence_index", "superseded_evidence_ids", "search_state"},
				map[string]any{
					"evidence_id":             schemaString("Durable evidence ID.", 128),
					"evidence_index":          map[string]any{"type": "integer", "minimum": 0},
					"superseded_evidence_ids": stringArraySchema("Evidence ID superseded by this evidence.", 50, 128),
					"search_state":            schemaEnum(domain.SearchProjectionStates()),
				},
			), 0, 100),
			"errors":                        submissionStatusErrorArraySchema(),
			"quarantine_expires_at":         nullableString("Fixed quarantine expiry.", 64),
			"replacement_window_expires_at": nullableString("Replacement window expiry.", 64),
			"awaiting_confirmation":         relationshipCorrectionConfirmationSchema(),
			"correction_result":             relationshipCorrectionResultSchema(),
		},
	)
}

func relationshipCorrectionConfirmationSchema() map[string]any {
	return closedObject(
		[]string{"confirmation_token", "expires_at", "candidates"},
		map[string]any{
			"confirmation_token": schemaString("One-round confirmation token.", 256),
			"expires_at":         schemaString("Confirmation expiry in RFC3339 format.", 64),
			"candidates": array(closedObject(
				[]string{"endpoint", "entity_id", "entity_kind", "canonical_name"},
				map[string]any{
					"endpoint":       schemaEnum([]string{"subject_entity", "object_entity"}),
					"entity_id":      schemaString("Same-team Entity candidate.", 128),
					"entity_kind":    schemaEnum(domain.EntityKinds()),
					"canonical_name": schemaString("Current canonical Entity name.", 256),
				},
			), 2, 40),
		},
	)
}

func relationshipCorrectionResultSchema() map[string]any {
	return closedObject(
		[]string{"original_relationship_id", "original_version", "successor_relationship_id", "successor_version", "reused_successor"},
		map[string]any{
			"original_relationship_id":  schemaString("Superseded Relationship ID.", 128),
			"original_version":          map[string]any{"type": "integer", "minimum": 1},
			"successor_relationship_id": schemaString("Created or reused successor Relationship ID.", 128),
			"successor_version":         map[string]any{"type": "integer", "minimum": 1},
			"reused_successor":          map[string]any{"type": "boolean"},
		},
	)
}

func retractEvidenceOutputSchema() map[string]any {
	return closedObject(
		[]string{
			"decision_id",
			"processing_state",
			"retracted_evidence_ids",
			"affected_relationship_count",
			"pending_relationship_count",
			"retained_active_relationship_count",
		},
		map[string]any{
			"decision_id":                        schemaString("Append-only lifecycle decision ID.", 128),
			"processing_state":                   schemaEnum([]string{"completed"}),
			"retracted_evidence_ids":             stringArraySchema("Retracted evidence ID.", 50, 128),
			"affected_relationship_count":        map[string]any{"type": "integer", "minimum": 0},
			"pending_relationship_count":         map[string]any{"type": "integer", "minimum": 0},
			"retained_active_relationship_count": map[string]any{"type": "integer", "minimum": 0},
		},
	)
}

func correctRelationshipOutputSchema() map[string]any {
	return closedObject(
		[]string{"submission_id", "submission_kind", "processing_state", "check_after_seconds", "status_tool", "correlation_id"},
		map[string]any{
			"submission_id":       schemaString("Correction submission ID.", 128),
			"submission_kind":     schemaEnum([]string{"relationship_correction"}),
			"processing_state":    schemaEnum([]string{"awaiting_confirmation", "completed", "rejected", "failed"}),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
			"status_tool":         schemaEnum([]string{ToolGetSubmissionStatus}),
			"correlation_id":      schemaString("Request correlation ID.", 128),
		},
	)
}

func recallMemoryOutputSchema() map[string]any {
	return closedObject(
		[]string{
			"recall_id", "results", "conflicts", "related_relationships",
			"related_communities", "related_hypotheses", "search_states", "degradations", "suggested_actions",
		},
		map[string]any{
			"recall_id":             schemaString("Recall event ID.", 128),
			"results":               array(recallResultSchema(), 0, 50),
			"conflicts":             array(recallConflictSchema(), 0, 20),
			"related_relationships": array(relatedRelationshipSchema(), 0, 20),
			"related_communities":   array(recallDiscoveryPathSchema(), 0, 10),
			"related_hypotheses":    array(hypothesisSummarySchema(), 0, 20),
			"search_states":         recallSearchStatesSchema(),
			"degradations":          array(recallDegradationSchema(), 0, 10),
			"suggested_actions":     array(recallSuggestedActionSchema(), 0, 2),
		},
	)
}

func recallSuggestedActionSchema() map[string]any {
	return closedObject(
		[]string{"tool", "guidance"},
		map[string]any{
			"tool":            schemaEnum([]string{ToolSubmitRecallSessionFeedback, ToolResolveDreamFeedback}),
			"guidance":        schemaString("Bounded next-step guidance.", 512),
			"recall_event_id": schemaString("Recall event ID accepted by the feedback tool.", 128),
			"hypothesis_ids":  stringArraySchema("Hypothesis IDs returned by this recall.", 20, 128),
		},
	)
}

func relatedRelationshipSchema() map[string]any {
	return closedObject(
		[]string{"relationship_id", "equivalent_relationship_ids", "subject", "predicate", "object", "polarity", "evidence_ids"},
		map[string]any{
			"relationship_id":             schemaString("Representative Relationship ID.", 128),
			"equivalent_relationship_ids": stringArraySchema("Same semantic group Relationship ID.", 20, 128),
			"subject":                     entityHandleSchema(),
			"predicate":                   schemaString("Registered predicate key.", 128),
			"object":                      semanticObjectSchema(),
			"polarity":                    schemaEnum([]string{"+", "-"}),
			"evidence_ids":                stringArraySchema("Evidence ID supporting this Relationship.", 50, 128),
			"search_state":                schemaEnum(domain.SearchProjectionStates()),
		},
	)
}

func recallSearchStatesSchema() map[string]any {
	return closedObject(
		[]string{"evidence", "relationships"},
		map[string]any{
			"evidence":      schemaEnum(domain.SearchProjectionStates()),
			"relationships": schemaEnum(domain.SearchProjectionStates()),
		},
	)
}

func recallDegradationSchema() map[string]any {
	return closedObject(
		[]string{"frontier", "code", "message"},
		map[string]any{
			"frontier":         schemaEnum([]string{"evidence", "relationships", "communities", "hypotheses", "feedback"}),
			"required_failure": map[string]any{"type": "boolean"},
			"optional":         map[string]any{"type": "boolean"},
			"code":             schemaString("Stable bounded degradation code.", 128),
			"message":          schemaString("Safe bounded degradation message.", 512),
		},
	)
}

func recallResultSchema() map[string]any {
	return closedObject(
		[]string{"evidence_id", "context"},
		map[string]any{
			"evidence_id": schemaString("Evidence ID.", 128),
			"context":     schemaString("Bounded evidence context.", 2000),
		},
	)
}

func traceMemoryOutputSchema() map[string]any {
	required := []string{
		"relationship", "observations", "evidence_supports", "evidence_support_decision_events",
		"evidence", "evidence_lifecycle_events", "verification_events", "transitions", "conflicts",
		"cross_profile_references", "identity_corrections", "supersession_lineage",
		"semantic_nodes", "semantic_edges", "visited_entity_ids", "stopped_reason",
	}
	return closedObject(required, map[string]any{
		"relationship":                     traceRelationshipSchema(),
		"observations":                     array(traceObservationSchema(), 0, 500),
		"evidence_supports":                array(traceEvidenceSupportSchema(), 0, 500),
		"evidence_support_decision_events": array(traceSupportDecisionSchema(), 0, 500),
		"evidence":                         array(traceEvidenceSchema(), 0, 500),
		"evidence_lifecycle_events":        array(traceEvidenceLifecycleEventSchema(), 0, 500),
		"verification_events":              array(traceVerificationSchema(), 0, 500),
		"transitions":                      array(traceTransitionSchema(), 0, 500),
		"conflicts":                        array(traceConflictSchema(), 0, 500),
		"cross_profile_references":         array(crossProfileReferenceSchema(), 0, 500),
		"identity_corrections":             array(traceIdentityCorrectionSchema(), 0, 500),
		"supersession_lineage":             array(traceRelationshipSchema(), 0, 500),
		"semantic_nodes":                   array(semanticNodeSchema(), 0, 500),
		"semantic_edges":                   array(semanticEdgeSchema(), 0, 500),
		"visited_entity_ids":               stringArraySchema("Visited Entity ID.", 500, 128),
		"stopped_reason":                   nullableString("Trace budget stop reason.", 128),
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
