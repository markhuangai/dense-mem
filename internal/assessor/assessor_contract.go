package assessor

// SemanticAssessmentResponseSchema is the strict structured-output contract for
// the Evidence-only assessor. Request-dependent spans and allowlists are
// intentionally validated after decoding against the frozen request.
func SemanticAssessmentResponseSchema() map[string]any {
	return SemanticAssessmentResponseSchemaWithLimits(DefaultSemanticAssessmentLimits())
}

func SemanticAssessmentResponseSchemaWithLimits(limits SemanticAssessmentLimits) map[string]any {
	limits = normalizeSemanticAssessmentLimits(limits)
	schema := closedObject(
		[]string{"request_id", "evidence_security_results", "evidence_equivalence_results", "evidence_conflict_results"},
		map[string]any{
			"request_id":                   stringSchema(1, 128),
			"evidence_security_results":    semanticAssessmentEvidenceSecurityResultSchema(),
			"evidence_equivalence_results": semanticAssessmentEvidenceEquivalenceResultSchema(),
			"evidence_conflict_results":    semanticAssessmentEvidenceConflictResultSchema(),
		},
	)
	properties := schema["properties"].(map[string]any)
	properties["evidence_security_results"].(map[string]any)["maxItems"] = limits.MaxEvidenceItems
	properties["evidence_equivalence_results"].(map[string]any)["maxItems"] = limits.MaxEvidenceItems
	return schema

}

func semanticAssessmentEvidenceConflictResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxEvidenceConflictResults,
		"items": closedObject(
			[]string{"positions"},
			map[string]any{
				"positions": map[string]any{
					"type": "array", "minItems": 2, "maxItems": SemanticAssessmentMaxEvidenceConflictPositions,
					"items": semanticAssessmentGroundedRangeSchema(),
				},
			},
		),
	}
}

func semanticAssessmentEvidenceEquivalenceResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxEvidenceSpans,
		"items": closedObject(
			[]string{"evidence_id", "action", "candidate_evidence_id"},
			map[string]any{
				"evidence_id":           stringSchema(1, 128),
				"action":                enumSchema([]string{"new", "reuse"}),
				"candidate_evidence_id": nullableStringSchema(128),
			},
		),
	}
}

func semanticAssessmentEvidenceSecurityResultSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": SemanticAssessmentMaxEvidenceSpans,
		"items": closedObject(
			[]string{"evidence_id", "decision", "signals"},
			map[string]any{
				"evidence_id": stringSchema(1, 128),
				"decision":    enumSchema([]string{"pass", "reject"}),
				"signals":     semanticAssessmentEvidenceSecuritySignalsSchema(),
			},
		),
	}
}

func semanticAssessmentEvidenceSecuritySignalsSchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 64,
		"items": closedObject(
			[]string{"kind", "start_ref", "end_ref"},
			map[string]any{
				"kind":      enumSchema(semanticSecurityKinds()),
				"start_ref": stringSchema(1, 128),
				"end_ref":   stringSchema(1, 128),
			},
		),
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
