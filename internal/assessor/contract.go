package assessor

import "github.com/markhuangai/dense-mem/internal/domain"

const (
	ProviderProposalSchemaName = "dense_mem_v2_provider_proposal"
)

func ProviderProposalSchema() map[string]any {
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
						"entity_kind":     enumSchema(domain.EntityKinds()),
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
			"relationship_kind":    enumSchema(domain.RelationshipKinds()),
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
			"type":    enumSchema(domain.ValueTypes()),
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
