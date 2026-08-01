package verifier

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func validateSubmissionAssessmentResponseRaw(raw []byte) []SemanticValidationError {
	top, errs := assessmentRawObject(raw, "", []string{"request_id", "security_assessments", "entity_results", "relationship_results"}, nil)
	if len(errs) > 0 {
		return errs
	}
	var securityAssessments []json.RawMessage
	if err := json.Unmarshal(top["security_assessments"], &securityAssessments); err != nil {
		return append(errs, semanticErr("security_assessments", "must be an array"))
	}
	for index, rawAssessment := range securityAssessments {
		path := fmt.Sprintf("security_assessments[%d]", index)
		assessment, assessmentErrors := assessmentRawObject(rawAssessment, path, []string{"evidence_id", "verdict", "signals", "justification"}, nil)
		errs = append(errs, assessmentErrors...)
		if len(assessmentErrors) == 0 {
			errs = append(errs, assessmentRawArrayObjects(assessment["signals"], path+".signals", []string{"evidence_id", "kind", "start", "end"}, nil)...)
		}
	}
	errs = append(errs, assessmentRawArrayObjects(top["entity_results"], "entity_results", []string{"ref", "surface", "kind", "evidence_id", "start", "end", "action", "candidate_entity_id", "confidence", "rationale"}, map[string]bool{"candidate_entity_id": true})...)

	var relationships []json.RawMessage
	if err := json.Unmarshal(top["relationship_results"], &relationships); err != nil {
		return append(errs, semanticErr("relationship_results", "must be an array"))
	}
	for index, relationshipRaw := range relationships {
		path := fmt.Sprintf("relationship_results[%d]", index)
		relationship, relationshipErrors := assessmentRawObject(relationshipRaw, path, []string{
			"ref", "subject_ref", "original_predicate", "predicate_status", "predicate_key", "predicate_version",
			"predicate_candidate", "object_ref", "object_value", "polarity", "modality", "evidence", "valid_from", "valid_to",
			"scope_status", "scope_key", "evidence_verdict", "temporal_verdict", "confidence", "rationale",
		}, map[string]bool{"predicate_key": true, "predicate_version": true, "predicate_candidate": true, "object_ref": true, "object_value": true, "valid_from": true, "valid_to": true, "scope_key": true})
		errs = append(errs, relationshipErrors...)
		if len(relationshipErrors) > 0 {
			continue
		}
		errs = append(errs, assessmentRawArrayObjects(relationship["evidence"], path+".evidence", []string{"evidence_id", "start", "end"}, nil)...)
		if !bytes.Equal(bytes.TrimSpace(relationship["object_value"]), []byte("null")) {
			_, valueErrors := assessmentRawObject(relationship["object_value"], path+".object_value", []string{"value_type", "canonical_value", "display", "unit"}, map[string]bool{"display": true, "unit": true})
			errs = append(errs, valueErrors...)
		}
		if !bytes.Equal(bytes.TrimSpace(relationship["predicate_candidate"]), []byte("null")) {
			_, candidateErrors := assessmentRawObject(relationship["predicate_candidate"], path+".predicate_candidate", []string{"predicate_key", "relationship_kind"}, nil)
			errs = append(errs, candidateErrors...)
		}
	}
	return errs
}
