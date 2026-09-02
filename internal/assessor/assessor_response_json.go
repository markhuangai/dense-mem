package assessor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

func validateSemanticAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	top, errs := assessmentRawObject(raw, "", []string{"request_id", "evidence_security_results", "entity_results", "relationship_results"}, nil)
	if len(errs) > 0 {
		return errs
	}
	var securityResults []json.RawMessage
	if err := json.Unmarshal(top["evidence_security_results"], &securityResults); err != nil {
		return append(errs, semanticErr("evidence_security_results", "must be an array"))
	}
	for i, resultRaw := range securityResults {
		path := fmt.Sprintf("evidence_security_results[%d]", i)
		result, resultErrs := assessmentRawObject(resultRaw, path, []string{"evidence_id", "decision", "signals"}, nil)
		errs = append(errs, resultErrs...)
		if len(resultErrs) > 0 {
			continue
		}
		errs = append(errs, assessmentRawArrayObjects(result["signals"], path+".signals", []string{"kind", "start_ref", "end_ref"}, nil)...)
	}
	errs = append(errs, assessmentRawArrayObjects(top["entity_results"], "entity_results", []string{"ref", "grounding_ref", "action", "candidate_entity_id"}, map[string]bool{"grounding_ref": true, "candidate_entity_id": true})...)

	var relationships []json.RawMessage
	if err := json.Unmarshal(top["relationship_results"], &relationships); err != nil {
		return append(errs, semanticErr("relationship_results", "must be an array"))
	}
	for i, relationshipRaw := range relationships {
		path := fmt.Sprintf("relationship_results[%d]", i)
		relationship, relationErrs := assessmentRawObject(relationshipRaw, path, []string{
			"ref", "disposition", "reason", "splits",
		}, map[string]bool{"reason": true})
		errs = append(errs, relationErrs...)
		if len(relationErrs) > 0 {
			continue
		}
		var splits []json.RawMessage
		if err := json.Unmarshal(relationship["splits"], &splits); err != nil {
			errs = append(errs, semanticErr(path+".splits", "must be an array"))
			continue
		}
		for j, splitRaw := range splits {
			errs = append(errs, validateSemanticAssessmentSplitRaw(splitRaw, fmt.Sprintf("%s.splits[%d]", path, j))...)
		}
	}
	return errs
}

func validateSemanticAssessmentSplitRaw(raw json.RawMessage, path string) []SemanticValidationError {
	split, errs := assessmentRawObject(raw, path, []string{
		"split_index", "subject_ref", "predicate_range", "predicate_status", "predicate_key", "predicate_version",
		"predicate_registration", "object_ref", "object_value", "value_range", "polarity", "support_ranges", "valid_from", "valid_to",
	}, map[string]bool{
		"predicate_key": true, "predicate_version": true, "predicate_registration": true,
		"object_ref": true, "object_value": true, "value_range": true, "valid_from": true, "valid_to": true,
	})
	if len(errs) > 0 {
		return errs
	}
	_, rangeErrs := assessmentRawObject(split["predicate_range"], path+".predicate_range", []string{"evidence_id", "start_ref", "end_ref"}, nil)
	errs = append(errs, rangeErrs...)
	errs = append(errs, assessmentRawArrayObjects(split["support_ranges"], path+".support_ranges", []string{"evidence_id", "start_ref", "end_ref"}, nil)...)
	if !bytes.Equal(bytes.TrimSpace(split["value_range"]), []byte("null")) {
		_, valueRangeErrs := assessmentRawObject(split["value_range"], path+".value_range", []string{"evidence_id", "start_ref", "end_ref"}, nil)
		errs = append(errs, valueRangeErrs...)
	}
	if !bytes.Equal(bytes.TrimSpace(split["object_value"]), []byte("null")) {
		_, valueErrs := assessmentRawObject(split["object_value"], path+".object_value", []string{"value_type", "canonical_value", "display", "unit"}, map[string]bool{"display": true, "unit": true})
		errs = append(errs, valueErrs...)
	}
	if !bytes.Equal(bytes.TrimSpace(split["predicate_registration"]), []byte("null")) {
		_, registrationErrs := assessmentRawObject(split["predicate_registration"], path+".predicate_registration", []string{
			"predicate_key", "relationship_kind", "current_cardinality",
		}, nil)
		errs = append(errs, registrationErrs...)
	}
	return errs
}

func assessmentRawArrayObjects(raw json.RawMessage, path string, fields []string, nullable map[string]bool) []SemanticValidationError {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return []SemanticValidationError{semanticErr(path, "must be an array")}
	}
	var errs []SemanticValidationError
	for i, value := range values {
		_, itemErrs := assessmentRawObject(value, fmt.Sprintf("%s[%d]", path, i), fields, nullable)
		errs = append(errs, itemErrs...)
	}
	return errs
}

func assessmentRawObject(raw json.RawMessage, path string, fields []string, nullable map[string]bool) (map[string]json.RawMessage, []SemanticValidationError) {
	if err := jsonstrict.RejectDuplicateFields(raw); err != nil {
		return nil, []SemanticValidationError{semanticErr(assessmentRawField(path, ""), err.Error())}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, []SemanticValidationError{semanticErr(assessmentRawField(path, ""), "must be an object")}
	}
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	var errs []SemanticValidationError
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			errs = append(errs, semanticErr(assessmentRawField(path, field), "is required"))
			continue
		}
		if !nullable[field] && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			errs = append(errs, semanticErr(assessmentRawField(path, field), "must not be null"))
		}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			errs = append(errs, semanticErr(assessmentRawField(path, field), "is unknown"))
		}
	}
	return object, errs
}

func assessmentRawField(path string, field string) string {
	if path == "" {
		return field
	}
	if field == "" {
		return path
	}
	return path + "." + field
}

func semanticAssessmentJoinedErrors(errs []SemanticValidationError) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}
