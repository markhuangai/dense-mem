package contract

import "slices"

type AssessmentValidation struct {
	Stage         string `json:"stage"`
	Code          string `json:"code"`
	FieldFamily   string `json:"field_family"`
	ExpectedCount *int   `json:"expected_count,omitempty"`
	ActualCount   *int   `json:"actual_count,omitempty"`
}

func (d *AssessmentValidation) Valid() bool {
	if d == nil {
		return true
	}
	if (d.ExpectedCount == nil) != (d.ActualCount == nil) ||
		(d.ExpectedCount != nil && (*d.ExpectedCount < 0 || *d.ActualCount < 0)) {
		return false
	}
	counted := slices.Contains([]string{"definition_limit", "item_count_mismatch", "equivalence_coverage", "output_limit"}, d.Code)
	if counted != (d.ExpectedCount != nil) {
		return false
	}
	switch d.Stage {
	case "decode":
		switch d.Code {
		case "strict_json", "object_decode":
			return d.FieldFamily == "response"
		case "array_decode":
			return slices.Contains([]string{"definitions", "items", "equivalence"}, d.FieldFamily)
		case "missing_field":
			return slices.Contains([]string{
				"request_id", "definitions", "items", "equivalence",
				"definitions.ref", "definitions.kind", "definitions.key", "definitions.label",
				"definitions.description", "definitions.aliases", "definitions.parent_ref", "definitions.base_entity_kind",
				"items.ref", "items.status", "items.definition_ref", "items.reason",
				"equivalence.ref", "equivalence.relation",
			}, d.FieldFamily)
		}
	case "validate":
		switch d.Code {
		case "request_id_mismatch":
			return d.FieldFamily == "request_id"
		case "missing_array":
			return slices.Contains([]string{"definitions", "items", "equivalence"}, d.FieldFamily)
		case "definition_limit", "definition_ref_invalid", "definition_ref_duplicate", "definition_ref_supplied",
			"definition_parent_ref", "definition_record", "definition_catalog", "definition_unused":
			return d.FieldFamily == "definitions"
		case "item_count_mismatch", "item_ref_unknown", "item_ref_duplicate", "item_reason_limit",
			"item_ambiguous_classification", "item_definition_ref", "item_locked_classification", "item_source_kind", "item_status":
			return d.FieldFamily == "items"
		case "equivalence_ref", "equivalence_relation", "equivalence_required_relation", "equivalence_coverage", "equivalence_transitivity":
			return d.FieldFamily == "equivalence"
		}
	case "limits":
		return d.Code == "output_limit" && d.FieldFamily == "output_tokens"
	}
	return false
}
