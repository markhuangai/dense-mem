package assessor

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidationDiagnosticCodesNeverChangeProviderFeedback(t *testing.T) {
	err := semanticCodedErr("relationship_results[0].splits[0].predicate_key", "is outside predicate allowlist", "predicate_outside_allowlist")
	encoded, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)
	require.JSONEq(t, `{"field":"relationship_results[0].splits[0].predicate_key","message":"is outside predicate allowlist"}`, string(encoded))
	require.Equal(t, "relationship_results[0].splits[0].predicate_key: is outside predicate allowlist", err.Error())
}

func TestEvidenceSecurityCoverageDiagnosticCodes(t *testing.T) {
	evidence := map[string]SemanticReviewEvidence{"submitted": {EvidenceID: "submitted"}}
	for _, test := range []struct {
		name    string
		results []SemanticAssessmentEvidenceSecurityResult
		code    string
	}{
		{"missing", nil, "evidence_result_missing"},
		{"unknown", []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "other"}}, "evidence_id_unknown"},
		{"empty", []SemanticAssessmentEvidenceSecurityResult{{}}, "evidence_id_required"},
		{"duplicate", []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass"}, {EvidenceID: "submitted", Decision: "pass"}}, "evidence_id_duplicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			issues := validateSemanticAssessmentEvidenceSecurityResults(test.results, evidence)
			codes := make([]string, len(issues))
			for i, issue := range issues {
				codes[i] = issue.Code
			}
			require.Contains(t, codes, test.code)
		})
	}
	require.Empty(t, validateSemanticAssessmentEvidenceSecurityResults([]SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "submitted", Decision: "pass"}}, evidence))
}

func TestSharedResponseShapeAndCorrectionErrors(t *testing.T) {
	_, errs := ValidateResponseObject([]byte(`{"required":null,"z":0,"a":0}`), "items[2]", []string{"required", "optional"}, nil, map[string]bool{"optional": true})
	require.Equal(t, []SemanticValidationError{
		{Field: "items[2].required", Message: "must not be null"},
		{Field: "items[2].a", Message: "is unknown"},
		{Field: "items[2].z", Message: "is unknown"},
	}, errs)
	_, errs = ValidateResponseObject([]byte(`{"required":null}`), "", []string{"required", "optional"}, map[string]bool{"required": true}, map[string]bool{"optional": true})
	require.Empty(t, errs)
	require.Equal(t, []SemanticValidationError{{Field: "items[1].ref", Message: "is required"}}, ValidateResponseArrayObjects([]byte(`[{"ref":"a"},{}]`), "items", []string{"ref"}, nil, nil))
	_, errs = ValidateResponseObject([]byte(`{"ref":"a","ref":"b"}`), "items[0]", []string{"ref"}, nil, nil)
	require.ErrorContains(t, errs[0], "duplicate JSON field")

	errs = make([]SemanticValidationError, SemanticAssessmentMaxCorrectionErrors+2)
	for i := range errs {
		errs[i] = SemanticValidationError{Field: fmt.Sprintf("items[%03d].ref", len(errs)-i), Message: "is duplicated", Code: "private"}
	}
	original := append([]SemanticValidationError(nil), errs...)
	bounded := BoundSemanticAssessmentCorrectionErrors(errs)
	require.Equal(t, original, errs)
	require.Len(t, bounded, SemanticAssessmentMaxCorrectionErrors)
	require.Equal(t, "items[001].ref", bounded[0].Field)
	require.Contains(t, bounded[len(bounded)-1].Message, "additional validation errors were omitted")
	encoded, err := json.Marshal(bounded)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	require.NotContains(t, string(encoded), "code")
	cause := errors.New("invalid complete response")
	require.ErrorIs(t, &SemanticAssessmentResponseValidationError{Errors: errs, Cause: cause}, cause)
}

func TestSharedCorrectionErrorsPreserveDistinctIssuesBeforeCountBound(t *testing.T) {
	issue := SemanticValidationError{Field: "items[0].ref", Message: "is duplicated", Code: "a"}
	errs := make([]SemanticValidationError, SemanticAssessmentMaxCorrectionErrors+1)
	for i := range errs {
		errs[i] = issue
	}
	differentCode := issue
	differentCode.Code = "b"
	differentMessage := issue
	differentMessage.Message = "is required"
	differentField := issue
	differentField.Field = "items[1].ref"
	errs = append(errs, differentField, differentCode, differentMessage, differentCode)
	original := append([]SemanticValidationError(nil), errs...)

	bounded := BoundSemanticAssessmentCorrectionErrors(errs)
	require.Equal(t, original, errs)
	require.Equal(t, []SemanticValidationError{issue, differentCode, differentMessage, differentField}, bounded)
	encoded, err := json.Marshal(bounded)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"code"`)
}
