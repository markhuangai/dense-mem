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
	return contractInput([]string{"evidence"}, map[string]any{
		"evidence": evidenceArraySchema(),
		"proposal": closedObject(nil, map[string]any{
			"entities":      entityProposalArraySchema(),
			"relationships": relationshipProposalArraySchema(),
		}),
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
				"supersedes_fragment_ids":  stringArraySchema("Evidence fragment ID to supersede.", 50, 128),
				"idempotency_key":          schemaString("Evidence retry key scoped to team and profile.", 128),
				"labels":                   stringArraySchema("Evidence label.", 20, 64),
				"metadata":                 boundedMap("Source-policy-approved metadata."),
			},
			"additionalProperties": false,
		},
	}
}

func entityProposalArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 100,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"ref", "name"},
			"properties": map[string]any{
				"ref":              nonEmptyStringSchema("Client-local Entity proposal ref.", 128),
				"name":             nonEmptyStringSchema("Evidence-supported name.", 256),
				"entity_kind":      schemaEnum(domain.EntityKinds()),
				"aliases":          stringArraySchema("Evidence-supported alias.", 20, 256),
				"identity_context": boundedMap("Evidence-supported identity discriminators."),
				"known_entity_id":  nullableString("Server-issued Entity candidate hint.", 128),
			},
			"additionalProperties": false,
		},
	}
}

func relationshipProposalArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 200,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"proposal_id", "subject_ref", "predicate", "evidence"},
			"oneOf": []any{
				map[string]any{
					"required": []string{"object_ref"},
					"not":      map[string]any{"required": []string{"object_value"}},
				},
				map[string]any{
					"required": []string{"object_value"},
					"not":      map[string]any{"required": []string{"object_ref"}},
				},
			},
			"properties": map[string]any{
				"proposal_id": nonEmptyStringSchema("Client-local Relationship proposal ref.", 128),
				"subject_ref": nonEmptyStringSchema("Entity proposal ref.", 128),
				"predicate":   nonEmptyStringSchema("Registered predicate name.", 128),
				"object_ref":  schemaString("Entity proposal ref.", 128),
				"object_value": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"type":    schemaEnum(domain.ValueTypes()),
						"value":   map[string]any{"type": []any{"string", "number", "boolean"}},
						"display": schemaString("Optional display form.", 1024),
						"unit":    schemaString("Optional unit.", 128),
					},
					"required": []string{"type", "value"},
				},
				"polarity":          schemaEnum([]string{"+", "-"}),
				"modality":          schemaEnum([]string{"statement", "question", "proposal", "speculation", "quoted"}),
				"valid_from":        nullableDateTime("Evidence-supported validity start."),
				"valid_to":          nullableDateTime("Evidence-supported validity end."),
				"correction_target": relationshipCorrectionTargetSchema(),
				"conflict_context":  relationshipConflictContextSchema(),
				"client_comment":    nullableString("Non-authoritative extraction note.", 1000),
				"evidence": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": 20,
					"items": map[string]any{
						"type":                 "object",
						"required":             []string{"evidence_index", "start", "end"},
						"additionalProperties": false,
						"properties": map[string]any{
							"evidence_index": map[string]any{"type": "integer", "minimum": 0},
							"start":          map[string]any{"type": "integer", "minimum": 0},
							"end":            map[string]any{"type": "integer", "minimum": 0},
						},
					},
				},
			},
			"additionalProperties": false,
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
		"reason":   schemaString("Bounded lifecycle feedback reason.", 1000),
		"evidence": evidenceArraySchema(),
		"proposal": closedObject(nil, map[string]any{
			"entities":      entityProposalArraySchema(),
			"relationships": relationshipProposalArraySchema(),
		}),
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

func placementItemSchema() map[string]any {
	return closedObject(
		[]string{"item_id", "evidence_id", "version", "evidence_index", "category", "search_state", "relationship_outcomes", "errors"},
		map[string]any{
			"item_id":               schemaString("Stable placement item ID.", 128),
			"evidence_id":           schemaString("Durable evidence fragment ID.", 128),
			"version":               map[string]any{"type": "integer", "minimum": 1},
			"evidence_index":        map[string]any{"type": "integer", "minimum": 0},
			"category":              schemaEnum(domain.EvidenceItemCategories()),
			"relationship_outcomes": relationshipOutcomeArraySchema(),
			"search_state":          schemaEnum(domain.SearchProjectionStates()),
			"errors":                placementErrorArraySchema(),
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
				"tier":                schemaEnum(domain.RelationshipTiers()),
				"relationship_status": schemaEnum(domain.RelationshipStatuses()),
				"category":            schemaEnum(domain.RelationshipOutcomeCategories()),
				"reason":              schemaString("Bounded placement explanation.", 1000),
				"review_task":         nullableString("Review task handle.", 128),
				"predicate_options":   array(predicateDefinitionSchema(), 0, 100),
			},
		),
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
		[]string{"recall_id", "results", "conflicts", "discovery_paths", "discovery_guidance", "related_hypotheses"},
		map[string]any{
			"recall_id":          schemaString("Recall event ID.", 128),
			"results":            array(recallResultSchema(), 0, 50),
			"conflicts":          array(recallConflictSchema(), 0, 20),
			"discovery_paths":    array(recallDiscoveryPathSchema(), 0, 50),
			"discovery_guidance": schemaString("Bounded follow-up guidance.", 1000),
			"related_hypotheses": array(hypothesisSummarySchema(), 0, 20),
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
		"relationship", "observations", "evidence_supports", "support_decision_events",
		"evidence_fragments", "verification_events", "transitions", "conflicts",
		"cross_profile_references", "identity_corrections", "supersession_lineage",
		"semantic_nodes", "semantic_edges", "visited_entity_ids", "stopped_reason",
	}
	return closedObject(required, map[string]any{
		"relationship":             traceRelationshipSchema(),
		"observations":             array(traceObservationSchema(), 0, 500),
		"evidence_supports":        array(traceEvidenceSupportSchema(), 0, 500),
		"support_decision_events":  array(traceSupportDecisionSchema(), 0, 500),
		"evidence_fragments":       array(traceEvidenceFragmentSchema(), 0, 500),
		"verification_events":      array(traceVerificationSchema(), 0, 500),
		"transitions":              array(traceTransitionSchema(), 0, 500),
		"conflicts":                array(traceConflictSchema(), 0, 500),
		"cross_profile_references": array(crossProfileReferenceSchema(), 0, 500),
		"identity_corrections":     array(traceIdentityCorrectionSchema(), 0, 500),
		"supersession_lineage":     array(traceRelationshipSchema(), 0, 500),
		"semantic_nodes":           array(semanticNodeSchema(), 0, 500),
		"semantic_edges":           array(semanticEdgeSchema(), 0, 500),
		"visited_entity_ids":       stringArraySchema("Visited Entity ID.", 500, 128),
		"stopped_reason":           nullableString("Trace budget stop reason.", 128),
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
