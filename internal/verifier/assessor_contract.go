package verifier

import "github.com/markhuangai/dense-mem/internal/domain"

// SemanticAssessmentResponseSchema is the strict structured-output contract for
// the integrated V2.6 assessor. Request-dependent spans and allowlists are
// intentionally validated after decoding against the frozen request.
func SemanticAssessmentResponseSchema() map[string]any {
	return closedObject(
		[]string{"request_id", "security_signals", "entity_results", "relationship_results"},
		map[string]any{
			"request_id":           stringSchema(1, 128),
			"security_signals":     semanticAssessmentSecuritySignalSchema(),
			"entity_results":       semanticAssessmentEntityResultSchema(),
			"relationship_results": semanticAssessmentRelationshipResultSchema(),
		},
	)
}

func semanticAssessmentEntityResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxEntityResults,
		"items": closedObject(
			[]string{"ref", "grounding_ref", "action", "candidate_entity_id"},
			map[string]any{
				"ref":                 stringSchema(1, 128),
				"grounding_ref":       nullableStringSchema(128),
				"action":              enumSchema(domain.EntityResolutionActions()),
				"candidate_entity_id": nullableStringSchema(128),
			},
		),
	}
}

func semanticAssessmentRelationshipResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxRelationshipResults,
		"items": closedObject(
			[]string{"ref", "disposition", "reason", "splits"},
			map[string]any{
				"ref":         stringSchema(1, 128),
				"disposition": enumSchema([]string{"stored", "not_supported"}),
				"reason":      nullableStringSchema(128),
				"splits": map[string]any{
					"type": "array", "maxItems": SemanticAssessmentMaxRelationshipSplits,
					"items": semanticAssessmentRelationshipSplitSchema(),
				},
			},
		),
	}
}

func semanticAssessmentRelationshipSplitSchema() map[string]any {
	return closedObject(
		[]string{
			"split_index", "subject_ref", "predicate_range", "predicate_status", "predicate_key", "predicate_version",
			"predicate_registration", "object_ref", "object_value", "value_range", "polarity", "support_ranges", "valid_from", "valid_to",
		},
		map[string]any{
			"split_index":       map[string]any{"type": "integer", "minimum": 0},
			"subject_ref":       stringSchema(1, 128),
			"predicate_range":   semanticAssessmentGroundedRangeSchema(),
			"predicate_status":  enumSchema([]string{"resolved", "registration_required"}),
			"predicate_key":     nullableStringSchema(128),
			"predicate_version": nullableIntegerSchema(1),
			"predicate_registration": map[string]any{
				"anyOf": []any{map[string]any{"type": "null"}, semanticAssessmentPredicateRegistrationSchema()},
			},
			"object_ref": nullableStringSchema(128),
			"object_value": map[string]any{
				"anyOf": []any{map[string]any{"type": "null"}, semanticAssessmentValueSchema()},
			},
			"value_range": map[string]any{
				"anyOf": []any{map[string]any{"type": "null"}, semanticAssessmentGroundedRangeSchema()},
			},
			"polarity":       enumSchema([]string{"+", "-"}),
			"support_ranges": semanticAssessmentGroundedRangeArraySchema(),
			"valid_from":     nullableDateTimeSchema(),
			"valid_to":       nullableDateTimeSchema(),
		},
	)
}

func semanticAssessmentPredicateRegistrationSchema() map[string]any {
	return closedObject(
		[]string{"predicate_key", "relationship_kind", "current_cardinality"},
		map[string]any{
			"predicate_key":       stringSchema(1, 128),
			"relationship_kind":   enumSchema(domain.RelationshipKinds()),
			"current_cardinality": enumSchema(domain.CurrentCardinalities()),
		},
	)
}

func semanticAssessmentValueSchema() map[string]any {
	return closedObject(
		[]string{"value_type", "canonical_value", "display", "unit"},
		map[string]any{
			"value_type":      enumSchema(domain.ValueTypes()),
			"canonical_value": stringSchema(1, 4096),
			"display":         nullableStringSchema(4096),
			"unit":            nullableStringSchema(128),
		},
	)
}

func semanticAssessmentGroundedRangeArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": SemanticAssessmentMaxEvidenceSpans,
		"items": semanticAssessmentGroundedRangeSchema(),
	}
}

func semanticAssessmentGroundedRangeSchema() map[string]any {
	return closedObject(
		[]string{"evidence_id", "start_ref", "end_ref"},
		map[string]any{
			"evidence_id": stringSchema(1, 128),
			"start_ref":   stringSchema(1, 128),
			"end_ref":     stringSchema(1, 128),
		},
	)
}

func semanticAssessmentSecuritySignalSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 64,
		"items": closedObject(
			[]string{"evidence_id", "kind", "start_ref", "end_ref"},
			map[string]any{
				"evidence_id": stringSchema(1, 128),
				"kind":        enumSchema(semanticSecurityKinds()),
				"start_ref":   stringSchema(1, 128),
				"end_ref":     stringSchema(1, 128),
			},
		),
	}
}

func nullableIntegerSchema(minimum int) map[string]any {
	return map[string]any{"type": []any{"integer", "null"}, "minimum": minimum}
}
