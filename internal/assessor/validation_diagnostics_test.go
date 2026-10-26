package assessor

import (
	"encoding/json"
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
