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
		"description": "Each relationship must include supports entries whose evidence_index values collectively cover every submitted evidence item. Spans use Unicode code-point offsets.",
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

func getMemoryPlacementInputSchema() map[string]any {
	return contractInput([]string{"ingest_id"}, map[string]any{
		"ingest_id": schemaString("Placement run ID returned by remember.", 128),
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

func resolveMemoryPlacementInputSchema() map[string]any {
	return contractInput([]string{"action", "idempotency_key"}, map[string]any{
		"action":            schemaEnum(domain.ResolveActions()),
		"ingest_id":         schemaString("Placement run ID.", 128),
		"placement_item_id": schemaString("Review item ID.", 128),
		"placement_item_version": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Observed placement item version from inspection; stale values are rejected.",
		},
		"observation_id":  schemaString("Unresolved observation ID.", 128),
		"relationship_id": schemaString("Caller-owned Relationship ID.", 128),
		"entity_ref":      schemaString("Client-local Entity proposal ref.", 128),
		"candidate_entity_id": schemaString(
			"Server-supplied candidate Entity ID selected for this profile.",
			128,
		),
		"predicate_key": schemaString("Server-supplied registered predicate key.", 128),
		"predicate_version": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Server-supplied predicate version selected for this observation.",
		},
		"message":         schemaString("Bounded reason or reviewer explanation.", 1000),
		"evidence":        evidenceArraySchema(),
		"idempotency_key": schemaString("Resolution retry key scoped to team and profile.", 128),
	})
}

func correctEntityResolutionInputSchema() map[string]any {
	return contractInput(
		[]string{"operation", "source_entity_id", "target_entity_id", "owned_observation_ids", "evidence", "dry_run", "idempotency_key"},
		map[string]any{
			"operation":             schemaEnum(domain.EntityCorrectionActions()),
			"source_entity_id":      schemaString("Caller-visible source Entity ID.", 128),
			"target_entity_id":      nullableString("Merge target Entity ID; null for split.", 128),
			"owned_observation_ids": stringArraySchema("Caller-owned observation ID.", 200, 128),
			"dry_run":               map[string]any{"type": "boolean"},
			"impact_token":          schemaString("Version-bound dry-run token required for apply.", 256),
			"evidence":              evidenceArraySchema(),
			"idempotency_key":       schemaString("Correction retry key scoped to team and profile.", 128),
		},
	)
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

func findMemoryPackCandidatesInputSchema() map[string]any {
	return contractInput([]string{"query"}, map[string]any{
		"query":          schemaString("Topic search query.", 512),
		"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"predicate_keys": stringArraySchema("Registered predicate key filter.", 100, 128),
	})
}

func exportMemoryPackInputSchema() map[string]any {
	return contractInput([]string{"name"}, map[string]any{
		"name":                 schemaString("Memory pack name.", 256),
		"description":          schemaString("Memory pack description.", 1024),
		"relationship_ids":     stringArraySchema("Relationship ID to export.", 500, 128),
		"include_evidence":     map[string]any{"type": "boolean"},
		"include_entity_names": map[string]any{"type": "boolean"},
	})
}

func inspectMemoryPackInputSchema() map[string]any {
	return contractInput([]string{"mode"}, map[string]any{
		"artifact_json":   schemaString("Memory-pack JSON artifact.", memoryPackArtifactMaxLength),
		"url":             schemaString("HTTPS URL.", 2048),
		"expected_sha256": schemaString("Expected canonical SHA-256.", 64),
		"mode":            schemaEnum([]string{"review", "trusted"}),
	})
}

func importMemoryPackInputSchema() map[string]any {
	return contractInput([]string{"mode"}, map[string]any{
		"artifact_json":   schemaString("Memory-pack JSON artifact.", memoryPackArtifactMaxLength),
		"url":             schemaString("HTTPS URL.", 2048),
		"expected_sha256": schemaString("Expected canonical SHA-256.", 64),
		"mode":            schemaEnum([]string{"review", "trusted"}),
		"conflict_decisions": array(closedObject(
			[]string{"item_id", "decision"},
			map[string]any{
				"item_id":          schemaString("Artifact-local item ID.", 128),
				"decision":         schemaEnum([]string{"skip", "import_for_review", "map_entity", "confirm_new_entity", "accept_source_authority"}),
				"target_entity_id": schemaString("Server-supplied same-team Entity candidate.", 128),
				"evidence":         evidenceArraySchema(),
			},
		), 0, 500),
		"idempotency_key": schemaString("Import retry key scoped to team and profile.", 128),
	})
}

func rollbackMemoryPackImportInputSchema() map[string]any {
	return contractInput([]string{"import_id", "dry_run"}, map[string]any{
		"import_id":    schemaString("Memory-pack import ID.", 128),
		"dry_run":      map[string]any{"type": "boolean"},
		"impact_token": schemaString("Version-bound rollback dry-run token required for apply.", 256),
	})
}

func rememberOutputSchema() map[string]any {
	return closedObject(
		[]string{"ingest_id", "processing_state", "check_after_seconds", "status_tool", "correlation_id"},
		map[string]any{
			"ingest_id":           schemaString("Placement run ID.", 128),
			"processing_state":    schemaEnum(domain.PlacementRunStatuses()),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
			"status_tool":         schemaEnum([]string{ToolGetMemoryPlacement}),
			"correlation_id":      schemaString("Request correlation ID.", 128),
		},
	)
}

func placementRunOutputSchema() map[string]any {
	return closedObject(
		[]string{"ingest_id", "processing_state", "search_state", "items", "errors"},
		map[string]any{
			"ingest_id":        schemaString("Placement run ID.", 128),
			"processing_state": schemaEnum(domain.PlacementRunStatuses()),
			"search_state":     schemaEnum(domain.SearchProjectionStates()),
			"items":            array(placementItemSchema(), 0, 20),
			"errors":           placementErrorArraySchema(),
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

func placementItemSchema() map[string]any {
	return closedObject(
		[]string{"item_id", "evidence_id", "superseded_evidence_ids", "version", "evidence_index", "category", "search_state", "relationship_outcomes", "review_tasks", "errors"},
		map[string]any{
			"item_id":                 schemaString("Stable placement item ID.", 128),
			"evidence_id":             schemaString("Durable evidence ID.", 128),
			"superseded_evidence_ids": stringArraySchema("Evidence ID superseded by this replacement.", 50, 128),
			"version":                 map[string]any{"type": "integer", "minimum": 1},
			"evidence_index":          map[string]any{"type": "integer", "minimum": 0},
			"category":                schemaEnum(domain.EvidenceItemCategories()),
			"relationship_outcomes":   relationshipOutcomeArraySchema(),
			"review_tasks":            placementReviewTaskArraySchema(),
			"search_state":            schemaEnum(domain.SearchProjectionStates()),
			"errors":                  placementErrorArraySchema(),
		},
	)
}

func relationshipOutcomeArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 200,
		"items": closedObject(
			[]string{"proposal_id", "observation_id", "owner_profile_id", "category", "reason"},
			map[string]any{
				"proposal_id":         nullableString("Client proposal ID when supplied.", 128),
				"observation_id":      nullableString("Durable observation ID when one exists.", 128),
				"relationship_id":     nullableString("Relationship ID when one exists.", 128),
				"owner_profile_id":    schemaString("Relationship owner profile ID.", 128),
				"relationship_status": schemaEnum(domain.RelationshipStatuses()),
				"category":            schemaEnum(domain.RelationshipOutcomeCategories()),
				"reason":              schemaString("Bounded placement explanation.", 1000),
				"review_task":         nullableString("Review task handle.", 128),
				"predicate_options":   array(predicateDefinitionSchema(), 0, 100),
				"confidence_gate":     schemaEnum([]string{"meets_write_threshold", "below_write_threshold", "not_applicable"}),
				"policy_version":      nullableString("Bounded assessment policy version.", 256),
			},
		),
	}
}

func placementReviewTaskArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 300,
		"items": closedObject(
			[]string{"review_task_id", "version", "kind", "status", "question", "options", "guidance", "expires_at"},
			map[string]any{
				"review_task_id": schemaString("Stable semantic review task ID.", 128),
				"version":        map[string]any{"type": "integer", "minimum": 1},
				"kind":           schemaEnum([]string{"support_confidence", "identity", "predicate", "scope", "time"}),
				"status":         schemaEnum([]string{"open", "acknowledged", "resolved", "canceled", "expired"}),
				"question":       schemaString("Exact bounded semantic review question.", 1000),
				"options":        array(placementReviewOptionSchema(), 0, 100),
				"guidance":       schemaString("Bounded semantic review guidance.", 2000),
				"expires_at":     map[string]any{"type": []any{"string", "null"}, "format": "date-time"},
			},
		),
	}
}

func placementReviewOptionSchema() map[string]any {
	return map[string]any{
		"x-enforce-one-of": true,
		"oneOf": []any{
			closedObject(
				[]string{"entity_id", "canonical_name", "kind"},
				map[string]any{
					"entity_id":      schemaString("Server-supplied Entity ID.", 128),
					"canonical_name": schemaString("Server-supplied Entity canonical name.", 256),
					"kind":           schemaEnum(domain.EntityKinds()),
				},
			),
			closedObject(
				[]string{"predicate_key", "version", "aliases"},
				map[string]any{
					"predicate_key": schemaString("Server-supplied predicate key.", 128),
					"version":       map[string]any{"type": "integer", "minimum": 1},
					"aliases":       stringArraySchema("Server-supplied predicate alias.", 20, 256),
				},
			),
			closedObject(
				[]string{"action"},
				map[string]any{
					"action": schemaEnum([]string{"submit_new_evidence"}),
				},
			),
		},
	}
}

func resolveMemoryPlacementOutputSchema() map[string]any {
	return closedObject(
		[]string{"decision_id", "processing_state"},
		map[string]any{
			"decision_id":         schemaString("Append-only decision ID.", 128),
			"ingest_id":           schemaString("Follow-up placement run ID.", 128),
			"processing_state":    schemaEnum(domain.PlacementRunStatuses()),
			"impact_summary":      schemaString("Bounded before/after summary.", 1000),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
		},
	)
}

func correctEntityResolutionOutputSchema() map[string]any {
	return closedObject(
		[]string{
			"dry_run",
			"selected_observation_ids",
			"blocked_observation_ids",
			"relationship_changes",
			"entity_candidates",
			"unchanged_cross_profile_references",
		},
		map[string]any{
			"dry_run":                            map[string]any{"type": "boolean"},
			"impact_token":                       schemaString("Expiring version-bound apply token.", 256),
			"selected_observation_ids":           stringArraySchema("Observation selected for change.", 200, 128),
			"blocked_observation_ids":            stringArraySchema("Observation blocked from change.", 200, 128),
			"relationship_changes":               array(correctionRelationshipChangeSchema(), 0, 500),
			"entity_candidates":                  array(correctionEntityCandidateSchema(), 0, 100),
			"unchanged_cross_profile_references": array(crossProfileReferenceSchema(), 0, 500),
		},
	)
}

func recallMemoryOutputSchema() map[string]any {
	return closedObject(
		[]string{
			"recall_id", "results", "conflicts", "related_relationships",
			"related_communities", "related_hypotheses", "search_states", "degradations",
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
			"frontier":         schemaEnum([]string{"evidence", "relationships", "communities", "hypotheses"}),
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

func predicateDefinitionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"predicate_key",
			"version",
			"relationship_kind",
			"current_cardinality",
			"lifecycle_state",
		},
		"properties": map[string]any{
			"predicate_key":         schemaString("Immutable predicate key.", 128),
			"aliases":               stringArraySchema("Predicate alias.", 50, 128),
			"allowed_subject_kinds": stringArraySchema("Allowed subject Entity kind.", 20, 64),
			"allowed_object_kinds": stringArraySchema(
				"Allowed object Entity kind or Value type.",
				20,
				64,
			),
			"relationship_kind":   schemaEnum(domain.RelationshipKinds()),
			"current_cardinality": schemaEnum(domain.CurrentCardinalities()),
			"lifecycle_state":     schemaEnum(domain.PredicateLifecycleStates()),
			"version":             map[string]any{"type": "integer", "minimum": 1},
		},
	}
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
