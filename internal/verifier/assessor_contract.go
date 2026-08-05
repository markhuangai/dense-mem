package verifier

import "github.com/markhuangai/dense-mem/internal/domain"

// SemanticAssessmentResponseSchema is the strict structured-output contract for
// the integrated V2.4 assessor. Request-dependent spans and allowlists are
// intentionally validated after decoding against the frozen request.
func SemanticAssessmentResponseSchema() map[string]any {
	return closedObject(
		[]string{"request_id", "security_signals", "entity_results", "relationship_results"},
		map[string]any{
			"request_id":           stringSchema(1, 128),
			"security_signals":     semanticSecuritySignalSchema(),
			"entity_results":       semanticAssessmentEntityResultSchema(),
			"relationship_results": semanticAssessmentRelationshipResultSchema(),
		},
	)
}

func semanticAssessmentEntityResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxEntityResults,
		"items": closedObject(
			[]string{"ref", "surface", "kind", "evidence_id", "start", "end", "action", "candidate_entity_id", "confidence", "rationale"},
			map[string]any{
				"ref":                 stringSchema(1, 128),
				"surface":             stringSchema(1, 1000),
				"kind":                enumSchema(domain.EntityKinds()),
				"evidence_id":         stringSchema(1, 128),
				"start":               integerSchema(0, -1),
				"end":                 integerSchema(1, -1),
				"action":              enumSchema(domain.EntityResolutionActions()),
				"candidate_entity_id": nullableStringSchema(128),
				"confidence":          numberSchema(0, 1),
				"rationale":           stringSchema(1, 1000),
			},
		),
	}
}

func semanticAssessmentRelationshipResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxRelationshipResults,
		"items": closedObject(
			[]string{
				"ref", "subject_ref", "original_predicate", "predicate_status", "predicate_key", "predicate_version",
				"object_ref", "object_value", "polarity", "modality", "evidence", "valid_from", "valid_to",
				"scope_status", "scope_key", "evidence_verdict", "temporal_verdict", "confidence", "rationale",
			},
			map[string]any{
				"ref":                stringSchema(1, 128),
				"subject_ref":        stringSchema(1, 128),
				"original_predicate": stringSchema(1, 256),
				"predicate_status":   enumSchema([]string{"resolved", "registration_required", "needs_review"}),
				"predicate_key":      nullableStringSchema(128),
				"predicate_version":  nullableIntegerSchema(1),
				"object_ref":         nullableStringSchema(128),
				"object_value": map[string]any{
					"anyOf": []any{map[string]any{"type": "null"}, semanticAssessmentValueSchema()},
				},
				"polarity":         enumSchema([]string{"+", "-"}),
				"modality":         enumSchema([]string{"statement", "question", "proposal", "speculation", "quoted"}),
				"evidence":         semanticAssessmentEvidenceSpanSchema(),
				"valid_from":       nullableDateTimeSchema(),
				"valid_to":         nullableDateTimeSchema(),
				"scope_status":     enumSchema([]string{"resolved", "absent", "needs_review"}),
				"scope_key":        nullableStringSchema(256),
				"evidence_verdict": enumSchema(domain.VerificationVerdicts()),
				"temporal_verdict": enumSchema([]string{"entailed", "absent", "ambiguous", "contradicted"}),
				"confidence":       numberSchema(0, 1),
				"rationale":        stringSchema(1, 1000),
			},
		),
	}
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

func semanticAssessmentEvidenceSpanSchema() map[string]any {
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": SemanticAssessmentMaxEvidenceSpans,
		"items": closedObject(
			[]string{"evidence_id", "start", "end"},
			map[string]any{
				"evidence_id": stringSchema(1, 128),
				"start":       integerSchema(0, -1),
				"end":         integerSchema(1, -1),
			},
		),
	}
}

func nullableIntegerSchema(minimum int) map[string]any {
	return map[string]any{"type": []any{"integer", "null"}, "minimum": minimum}
}
