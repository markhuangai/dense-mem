package registry

// claimObjectSchema mirrors dto.ClaimResponse. Hand-built to avoid reflection.
func claimObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"claim_id":           map[string]any{"type": "string"},
			"team_id":            map[string]any{"type": "string"},
			"subject":            map[string]any{"type": "string"},
			"predicate":          map[string]any{"type": "string"},
			"object":             map[string]any{"type": "string"},
			"modality":           map[string]any{"type": "string"},
			"polarity":           map[string]any{"type": "string"},
			"speaker":            map[string]any{"type": "string"},
			"span_start":         map[string]any{"type": "integer"},
			"span_end":           map[string]any{"type": "integer"},
			"valid_from":         map[string]any{"type": "string", "format": "date-time"},
			"valid_to":           map[string]any{"type": "string", "format": "date-time"},
			"recorded_at":        map[string]any{"type": "string", "format": "date-time"},
			"recorded_to":        map[string]any{"type": "string", "format": "date-time"},
			"extract_conf":       map[string]any{"type": "number"},
			"resolution_conf":    map[string]any{"type": "number"},
			"source_quality":     map[string]any{"type": "number"},
			"entailment_verdict": map[string]any{"type": "string"},
			"status":             map[string]any{"type": "string"},
			"extraction_model":   map[string]any{"type": "string"},
			"extraction_version": map[string]any{"type": "string"},
			"verifier_model":     map[string]any{"type": "string"},
			"pipeline_run_id":    map[string]any{"type": "string"},
			"content_hash":       map[string]any{"type": "string"},
			"idempotency_key":    map[string]any{"type": "string"},
			"classification":     map[string]any{"type": "object"},
			"supported_by":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"evidence":           map[string]any{"type": "array", "items": evidenceObjectSchema()},
		},
	}
}

// factObjectSchema mirrors dto.FactResponse. Hand-built to avoid reflection.
func factObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fact_id":                        map[string]any{"type": "string"},
			"team_id":                        map[string]any{"type": "string"},
			"subject":                        map[string]any{"type": "string"},
			"predicate":                      map[string]any{"type": "string"},
			"object":                         map[string]any{"type": "string"},
			"status":                         map[string]any{"type": "string"},
			"truth_score":                    map[string]any{"type": "number"},
			"valid_from":                     map[string]any{"type": "string", "format": "date-time"},
			"valid_to":                       map[string]any{"type": "string", "format": "date-time"},
			"recorded_at":                    map[string]any{"type": "string", "format": "date-time"},
			"recorded_to":                    map[string]any{"type": "string", "format": "date-time"},
			"retracted_at":                   map[string]any{"type": "string", "format": "date-time"},
			"last_confirmed_at":              map[string]any{"type": "string", "format": "date-time"},
			"promoted_from_claim_id":         map[string]any{"type": "string"},
			"classification":                 map[string]any{"type": "object"},
			"classification_lattice_version": map[string]any{"type": "string"},
			"source_quality":                 map[string]any{"type": "number"},
			"labels":                         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata":                       map[string]any{"type": "object"},
			"evidence":                       map[string]any{"type": "array", "items": evidenceObjectSchema()},
		},
	}
}
