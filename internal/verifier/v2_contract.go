package verifier

import "github.com/markhuangai/dense-mem/internal/domain"

const V2ProviderProposalSchemaName = "dense_mem_v2_provider_proposal"

func V2ProviderProposalSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"evidence", "entity_proposals", "relationship_proposals"},
		"properties": map[string]any{
			"predicate_options": predicateOptionArraySchema(),
			"evidence": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": 20,
				"items": map[string]any{
					"type":                 "object",
					"required":             []string{"evidence_index", "content"},
					"additionalProperties": false,
					"properties": map[string]any{
						"evidence_index": map[string]any{"type": "integer", "minimum": 0},
						"content":        map[string]any{"type": "string", "maxLength": 999},
					},
				},
			},
			"entity_proposals": map[string]any{
				"type":     "array",
				"maxItems": 100,
				"items": map[string]any{
					"type":                 "object",
					"required":             []string{"ref", "name", "kind", "evidence"},
					"additionalProperties": false,
					"properties": map[string]any{
						"ref":      map[string]any{"type": "string", "maxLength": 128},
						"name":     map[string]any{"type": "string", "maxLength": 256},
						"kind":     map[string]any{"type": "string", "enum": domain.V2EntityKinds()},
						"aliases":  stringArraySchema(20, 256),
						"evidence": evidenceSpanArraySchema(),
					},
				},
			},
			"relationship_proposals": map[string]any{
				"type":     "array",
				"maxItems": 200,
				"items": map[string]any{
					"type":                 "object",
					"required":             []string{"ref", "subject_ref", "original_predicate", "evidence"},
					"additionalProperties": false,
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
						"ref":                map[string]any{"type": "string", "maxLength": 128},
						"subject_ref":        map[string]any{"type": "string", "maxLength": 128},
						"original_predicate": map[string]any{"type": "string", "maxLength": 128},
						"predicate_key":      map[string]any{"type": "string", "maxLength": 128},
						"predicate_version":  map[string]any{"type": "integer", "minimum": 1},
						"object_ref":         map[string]any{"type": "string", "maxLength": 128},
						"object_value": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"type", "value"},
							"properties": map[string]any{
								"type":  map[string]any{"type": "string", "enum": domain.V2ValueTypes()},
								"value": map[string]any{"type": "string", "maxLength": 1024},
							},
						},
						"polarity": map[string]any{"type": "string", "enum": []string{"+", "-"}},
						"evidence": evidenceSpanArraySchema(),
					},
				},
			},
			"review_items": map[string]any{
				"type":     "array",
				"maxItems": 200,
				"items": map[string]any{
					"type":                 "object",
					"required":             []string{"category", "reason"},
					"additionalProperties": false,
					"properties": map[string]any{
						"category": map[string]any{
							"type": "string",
							"enum": v2ProviderReviewCategories(),
						},
						"reason": map[string]any{"type": "string", "maxLength": 1000},
					},
				},
			},
		},
	}
}

func v2ProviderReviewCategories() []string {
	return []string{
		string(domain.V2OutcomeRelationshipPendingEvidence),
		string(domain.V2OutcomeRelationshipNeedsReview),
		string(domain.V2OutcomePredicateNeedsReview),
		string(domain.V2OutcomeIdentityNeedsReview),
	}
}

func predicateOptionArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 100,
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required": []string{
				"predicate_key",
				"version",
				"relationship_kind",
				"current_cardinality",
			},
			"properties": map[string]any{
				"predicate_key":         map[string]any{"type": "string", "maxLength": 128},
				"aliases":               stringArraySchema(50, 128),
				"allowed_subject_kinds": stringArraySchema(20, 64),
				"allowed_object_kinds":  stringArraySchema(20, 64),
				"relationship_kind": map[string]any{
					"type": "string",
					"enum": domain.V2RelationshipKinds(),
				},
				"current_cardinality": map[string]any{
					"type": "string",
					"enum": domain.V2CurrentCardinalities(),
				},
				"version": map[string]any{"type": "integer", "minimum": 1},
			},
		},
	}
}

func evidenceSpanArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 20,
		"items": map[string]any{
			"type":                 "object",
			"required":             []string{"evidence_index", "quote"},
			"additionalProperties": false,
			"properties": map[string]any{
				"evidence_index": map[string]any{"type": "integer", "minimum": 0},
				"quote":          map[string]any{"type": "string", "maxLength": 999},
				"span_start":     map[string]any{"type": "integer", "minimum": 0},
				"span_end":       map[string]any{"type": "integer", "minimum": 0},
			},
		},
	}
}

func stringArraySchema(maxItems int, maxLength int) map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": maxItems,
		"items":    map[string]any{"type": "string", "maxLength": maxLength},
	}
}
