package assessor

import (
	"encoding/json"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

type SemanticAssessmentResponseValidationError = jsonstrict.ValidationError

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
	return jsonstrict.ValidateResponseArrayObjects(raw, path, fields, nullable, nil)
}

func assessmentRawObject(raw json.RawMessage, path string, fields []string, nullable map[string]bool) (map[string]json.RawMessage, []SemanticValidationError) {
	return jsonstrict.ValidateResponseObject(raw, path, fields, nullable, nil)
}
