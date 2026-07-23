package verifier

import "github.com/markhuangai/dense-mem/internal/domain"

const (
	V2ProviderProposalSchemaName = "dense_mem_v2_provider_proposal"
	V2VerifierResponseSchemaName = "dense_mem_v2_verifier_response"
)

func V2ProviderProposalSchema() map[string]any {
	return closedObject(
		[]string{"predicate_options", "entity_proposals", "relationship_proposals"},
		map[string]any{
			"predicate_options": stringArraySchema(100, 128),
			"entity_proposals": map[string]any{
				"type": "array", "maxItems": 100,
				"items": closedObject(
					[]string{"ref", "name", "entity_kind", "aliases", "known_entity_id", "evidence"},
					map[string]any{
						"ref":             stringSchema(1, 128),
						"name":            stringSchema(1, 256),
						"entity_kind":     enumSchema(domain.V2EntityKinds()),
						"aliases":         stringArraySchema(20, 256),
						"known_entity_id": nullableStringSchema(128),
						"evidence":        evidenceSpanArraySchema(),
					},
				),
			},
			"relationship_proposals": map[string]any{
				"type": "array", "maxItems": 200,
				"items": relationshipProposalSchema(),
			},
		},
	)
}

func V2VerifierResponseSchema() map[string]any {
	return closedObject(
		[]string{"request_id", "security_signals", "entity_results", "relationship_results"},
		map[string]any{
			"request_id": stringSchema(1, 128),
			"security_signals": map[string]any{
				"type": "array", "maxItems": 64,
				"items": closedObject(
					[]string{"evidence_id", "kind", "start", "end"},
					map[string]any{
						"evidence_id": stringSchema(1, 128),
						"kind": enumSchema([]string{
							"role_control_spoofing",
							"instruction_override",
							"prompt_secret_extraction",
							"tool_exfiltration",
							"obfuscated_instruction",
							"hidden_control_markup",
						}),
						"start": integerSchema(0, -1),
						"end":   integerSchema(0, -1),
					},
				),
			},
			"entity_results": map[string]any{
				"type": "array", "maxItems": 100,
				"items": entityResultSchema(),
			},
			"relationship_results": map[string]any{
				"type": "array", "maxItems": 200,
				"items": relationshipResultSchema(),
			},
		},
	)
}

func relationshipProposalSchema() map[string]any {
	return closedObject(
		[]string{
			"proposal_id",
			"subject_ref",
			"original_predicate",
			"predicate_candidates",
			"relationship_kind",
			"object_ref",
			"object_value",
			"polarity",
			"modality",
			"evidence",
			"valid_from",
			"valid_to",
			"client_comment",
		},
		map[string]any{
			"proposal_id":          stringSchema(1, 128),
			"subject_ref":          stringSchema(1, 128),
			"original_predicate":   stringSchema(1, 128),
			"predicate_candidates": stringArraySchema(100, 128),
			"relationship_kind":    enumSchema(domain.V2RelationshipKinds()),
			"object_ref":           stringSchema(0, 128),
			"object_value": map[string]any{
				"anyOf": []any{typedValueSchema(), map[string]any{"type": "null"}},
			},
			"polarity": optionalEnumSchema([]string{"+", "-"}),
			"modality": optionalEnumSchema([]string{
				"statement", "question", "proposal", "speculation", "quoted",
			}),
			"evidence":       evidenceSpanArraySchema(),
			"valid_from":     nullableDateTimeSchema(),
			"valid_to":       nullableDateTimeSchema(),
			"client_comment": nullableStringSchema(1000),
		},
	)
}

func entityResultSchema() map[string]any {
	return closedObject(
		[]string{"ref", "action", "candidate_entity_id", "confidence", "rationale"},
		map[string]any{
			"ref":                 stringSchema(1, 128),
			"action":              enumSchema([]string{"reuse", "create", "ambiguous"}),
			"candidate_entity_id": nullableStringSchema(128),
			"confidence":          numberSchema(0, 1),
			"rationale":           stringSchema(1, 1000),
		},
	)
}

func relationshipResultSchema() map[string]any {
	return closedObject(
		[]string{"ref", "predicate_status", "predicate_key", "evidence_verdict", "confidence", "rationale"},
		map[string]any{
			"ref":              stringSchema(1, 128),
			"predicate_status": enumSchema([]string{"resolved", "needs_review"}),
			"predicate_key":    nullableStringSchema(128),
			"evidence_verdict": enumSchema([]string{"entailed", "contradicted", "insufficient"}),
			"confidence":       numberSchema(0, 1),
			"rationale":        stringSchema(1, 1000),
		},
	)
}

func predicateOptionArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 100,
		"items": closedObject(
			[]string{"predicate_key", "version", "relationship_kind", "current_cardinality"},
			map[string]any{
				"predicate_key":         stringSchema(1, 128),
				"aliases":               stringArraySchema(50, 128),
				"allowed_subject_kinds": stringArraySchema(20, 64),
				"allowed_object_kinds":  stringArraySchema(20, 64),
				"relationship_kind":     enumSchema(domain.V2RelationshipKinds()),
				"current_cardinality":   enumSchema(domain.V2CurrentCardinalities()),
				"version":               integerSchema(1, -1),
			},
		),
	}
}

func evidenceSpanArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": 20,
		"items": closedObject(
			[]string{"evidence_index", "start", "end"},
			map[string]any{
				"evidence_index": integerSchema(0, 19),
				"start":          integerSchema(0, -1),
				"end":            integerSchema(0, -1),
			},
		),
	}
}

func typedValueSchema() map[string]any {
	return closedObject(
		[]string{"type", "value", "display", "unit"},
		map[string]any{
			"type":    enumSchema(domain.V2ValueTypes()),
			"value":   stringSchema(1, 4096),
			"display": stringSchema(0, 1024),
			"unit":    stringSchema(0, 128),
		},
	)
}

func closedObject(required []string, properties map[string]any) map[string]any {
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

func stringSchema(minLength int, maxLength int) map[string]any {
	schema := map[string]any{"type": "string"}
	if minLength > 0 {
		schema["minLength"] = minLength
	}
	if maxLength > 0 {
		schema["maxLength"] = maxLength
	}
	return schema
}

func nullableStringSchema(maxLength int) map[string]any {
	return map[string]any{"type": []any{"string", "null"}, "maxLength": maxLength}
}

func nullableDateTimeSchema() map[string]any {
	return map[string]any{"type": []any{"string", "null"}, "format": "date-time"}
}

func integerSchema(minimum int, maximum int) map[string]any {
	schema := map[string]any{"type": "integer", "minimum": minimum}
	if maximum >= 0 {
		schema["maximum"] = maximum
	}
	return schema
}

func numberSchema(minimum float64, maximum float64) map[string]any {
	return map[string]any{"type": "number", "minimum": minimum, "maximum": maximum}
}

func enumSchema(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func optionalEnumSchema(values []string) map[string]any {
	enumValues := make([]any, 0, len(values)+1)
	enumValues = append(enumValues, "")
	for _, value := range values {
		enumValues = append(enumValues, value)
	}
	return map[string]any{"type": "string", "enum": enumValues}
}

func stringArraySchema(maxItems int, maxLength int) map[string]any {
	return map[string]any{
		"type": "array", "maxItems": maxItems,
		"items": stringSchema(1, maxLength),
	}
}
