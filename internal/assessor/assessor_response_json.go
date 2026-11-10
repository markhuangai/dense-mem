package assessor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

type SemanticAssessmentResponseValidationError struct {
	Errors []SemanticValidationError
	Cause  error `json:"-"`
}

func (e *SemanticAssessmentResponseValidationError) Error() string {
	return semanticAssessmentJoinedErrors(e.Errors)
}

func (e *SemanticAssessmentResponseValidationError) Unwrap() error { return e.Cause }

func validateSemanticAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	top, errs := assessmentRawObject(raw, "", []string{"request_id", "evidence_security_results", "evidence_equivalence_results", "evidence_conflict_results"}, nil)
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
	var equivalenceResults []json.RawMessage
	if err := json.Unmarshal(top["evidence_equivalence_results"], &equivalenceResults); err != nil {
		return append(errs, semanticErr("evidence_equivalence_results", "must be an array"))
	}
	for i, resultRaw := range equivalenceResults {
		path := fmt.Sprintf("evidence_equivalence_results[%d]", i)
		_, resultErrs := assessmentRawObject(resultRaw, path, []string{"evidence_id", "action", "candidate_evidence_id"}, map[string]bool{"candidate_evidence_id": true})
		errs = append(errs, resultErrs...)
	}
	var conflictResults []json.RawMessage
	if err := json.Unmarshal(top["evidence_conflict_results"], &conflictResults); err != nil {
		return append(errs, semanticErr("evidence_conflict_results", "must be an array"))
	}
	for i, resultRaw := range conflictResults {
		path := fmt.Sprintf("evidence_conflict_results[%d]", i)
		result, resultErrs := assessmentRawObject(resultRaw, path, []string{"positions"}, nil)
		errs = append(errs, resultErrs...)
		if len(resultErrs) > 0 {
			continue
		}
		errs = append(errs, assessmentRawArrayObjects(result["positions"], path+".positions", []string{"evidence_id", "start_ref", "end_ref"}, nil)...)
	}

	return errs
}

func assessmentRawArrayObjects(raw json.RawMessage, path string, fields []string, nullable map[string]bool) []SemanticValidationError {
	return ValidateResponseArrayObjects(raw, path, fields, nullable, nil)
}

// ValidateResponseArrayObjects applies the shared closed-object checks to every indexed array entry.
func ValidateResponseArrayObjects(raw json.RawMessage, path string, fields []string, nullable, optional map[string]bool) []SemanticValidationError {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return []SemanticValidationError{semanticErr(path, "must be an array")}
	}
	var errs []SemanticValidationError
	for i, value := range values {
		_, itemErrs := ValidateResponseObject(value, fmt.Sprintf("%s[%d]", path, i), fields, nullable, optional)
		errs = append(errs, itemErrs...)
	}
	return errs
}

func assessmentRawObject(raw json.RawMessage, path string, fields []string, nullable map[string]bool) (map[string]json.RawMessage, []SemanticValidationError) {
	return ValidateResponseObject(raw, path, fields, nullable, nil)
}

// ValidateResponseObject checks duplicate, unknown, required and nullable fields without coercing values.
func ValidateResponseObject(raw json.RawMessage, path string, fields []string, nullable, optional map[string]bool) (map[string]json.RawMessage, []SemanticValidationError) {
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
			if optional[field] {
				continue
			}
			errs = append(errs, semanticErr(assessmentRawField(path, field), "is required"))
			continue
		}
		if !nullable[field] && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			errs = append(errs, semanticErr(assessmentRawField(path, field), "must not be null"))
		}
	}
	unknown := make([]string, 0)
	for field := range object {
		if _, ok := allowed[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	sort.Strings(unknown)
	for _, field := range unknown {
		errs = append(errs, semanticErr(assessmentRawField(path, field), "is unknown"))
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

// ResponseJSONValidationError retains the shared provider-facing JSON type error contract.
func ResponseJSONValidationError(err error, path string) SemanticValidationError {
	field := path
	if field == "" {
		field = "response"
	}
	message := "must be one complete JSON object matching the required field types"
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) && (typeError.Field != "" || path != "") {
		if typeError.Field != "" {
			field = assessmentRawField(path, typeError.Field)
		}
		message = "must match the required JSON type"
	}
	return semanticErr(field, message)
}

func semanticAssessmentJoinedErrors(errs []SemanticValidationError) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

// BoundSemanticAssessmentCorrectionErrors keeps distinct repair feedback deterministic and within the shared error-count bound.
func BoundSemanticAssessmentCorrectionErrors(errs []SemanticValidationError) []SemanticValidationError {
	bounded := append([]SemanticValidationError(nil), errs...)
	sort.Slice(bounded, func(i, j int) bool {
		if bounded[i].Field == bounded[j].Field {
			if bounded[i].Message == bounded[j].Message {
				return bounded[i].Code < bounded[j].Code
			}
			return bounded[i].Message < bounded[j].Message
		}
		return bounded[i].Field < bounded[j].Field
	})
	bounded = slices.Compact(bounded)
	if len(bounded) <= SemanticAssessmentMaxCorrectionErrors {
		return bounded
	}
	bounded = bounded[:SemanticAssessmentMaxCorrectionErrors-1]
	return append(bounded, SemanticValidationError{
		Field:   "response",
		Message: "additional validation errors were omitted; return one complete response matching the schema",
	})
}
