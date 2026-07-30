package verifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func validateSemanticAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	top, errs := assessmentRawObject(raw, "", []string{"request_id", "security_signals", "entity_results", "relationship_results"}, nil)
	if len(errs) > 0 {
		return errs
	}
	errs = append(errs, assessmentRawArrayObjects(top["security_signals"], "security_signals", []string{"evidence_id", "kind", "start", "end"}, nil)...)
	errs = append(errs, assessmentRawArrayObjects(top["entity_results"], "entity_results", []string{"ref", "surface", "kind", "evidence_id", "start", "end", "action", "candidate_entity_id", "confidence", "rationale"}, map[string]bool{"candidate_entity_id": true})...)

	var relationships []json.RawMessage
	if err := json.Unmarshal(top["relationship_results"], &relationships); err != nil {
		return append(errs, semanticErr("relationship_results", "must be an array"))
	}
	for i, relationshipRaw := range relationships {
		path := fmt.Sprintf("relationship_results[%d]", i)
		relationship, relationErrs := assessmentRawObject(relationshipRaw, path, []string{
			"ref", "subject_ref", "original_predicate", "predicate_status", "predicate_key", "predicate_version",
			"object_ref", "object_value", "polarity", "modality", "evidence", "valid_from", "valid_to",
			"scope_status", "scope_key", "evidence_verdict", "temporal_verdict", "confidence", "rationale",
		}, map[string]bool{"predicate_key": true, "predicate_version": true, "object_ref": true, "object_value": true, "valid_from": true, "valid_to": true, "scope_key": true})
		errs = append(errs, relationErrs...)
		if len(relationErrs) > 0 {
			continue
		}
		errs = append(errs, assessmentRawArrayObjects(relationship["evidence"], path+".evidence", []string{"evidence_id", "start", "end"}, nil)...)
		if !bytes.Equal(bytes.TrimSpace(relationship["object_value"]), []byte("null")) {
			_, valueErrs := assessmentRawObject(relationship["object_value"], path+".object_value", []string{"value_type", "canonical_value", "display", "unit"}, map[string]bool{"display": true, "unit": true})
			errs = append(errs, valueErrs...)
		}
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
