package assessor

import (
	"encoding/json"

	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentEvidenceSecurityResultErrorsUseMatchingResultIndex(t *testing.T) {
	request, limits := semanticAssessmentTestRequest(t)
	request.Evidence = append(request.Evidence, SemanticReviewEvidence{
		EvidenceID: "ev-2", FragmentID: "fragment-2", Content: "A second evidence item.",
	})
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	require.Empty(t, errs)

	response := semanticAssessmentTestResponse()
	response.EvidenceSecurityResults = []SemanticAssessmentEvidenceSecurityResult{
		{EvidenceID: "ev-2", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}},
		{EvidenceID: "ev-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}},
	}
	startRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	endRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 4)
	response.EvidenceSecurityResults[1].Decision = "reject"
	response.EvidenceSecurityResults[1].Signals = []SemanticAssessmentSecuritySignal{{
		EvidenceID: "ev-1", Kind: "instruction_override", StartRef: startRef, EndRef: endRef,
	}}

	_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Empty(t, validationErrors)
}

func TestDecodeSemanticAssessmentResponseRejectsRawShapeBoundaries(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "null required array",
			mutate: func(payload map[string]any) {
				payload["evidence_security_results"] = nil
			},
			want: "evidence_security_results: must not be null",
		},
		{
			name: "missing security result array",
			mutate: func(payload map[string]any) {
				delete(payload, "evidence_security_results")
			},
			want: "evidence_security_results: is required",
		},
		{
			name:   "retired result",
			mutate: func(payload map[string]any) { payload["entity_results"] = []any{} },
			want:   "entity_results: is unknown",
		},
		{
			name: "unknown nested security field",
			mutate: func(payload map[string]any) {
				payload["evidence_security_results"].([]any)[0].(map[string]any)["unknown"] = true
			},
			want: "evidence_security_results[0].unknown: is unknown",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := json.Marshal(semanticAssessmentTestResponse())
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			payload := map[string]any{}
			if err := json.Unmarshal(encoded, &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			testCase.mutate(payload)
			encoded, err = json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if _, err := DecodeSemanticAssessmentResponseJSON(encoded, DefaultSemanticAssessmentLimits()); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestSemanticAssessmentLimitNormalizationAndTokenizerFailure(t *testing.T) {
	defaults := DefaultSemanticAssessmentLimits()
	if got := normalizeSemanticAssessmentLimits(SemanticAssessmentLimits{}); got != defaults {
		t.Fatalf("normalized defaults = %#v, want %#v", got, defaults)
	}
	if _, err := CountTokens("Dense-Mem", "not-a-tokenizer"); err == nil || !strings.Contains(err.Error(), "not-a-tokenizer") {
		t.Fatalf("CountTokens() error = %v, want tokenizer error", err)
	}

}

func TestSemanticAssessmentEvidenceSecurityResultsRequireCompleteEvidenceCoverage(t *testing.T) {
	request, limits := semanticAssessmentPreparedTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(request, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	tests := []struct {
		name   string
		mutate func(*SemanticAssessmentResponse)
		want   string
	}{
		{name: "missing result", mutate: func(response *SemanticAssessmentResponse) { response.EvidenceSecurityResults = nil }, want: "evidence_security_results: is required"},
		{name: "duplicate result", mutate: func(response *SemanticAssessmentResponse) {
			response.EvidenceSecurityResults = append(response.EvidenceSecurityResults, response.EvidenceSecurityResults[0])
		}, want: "evidence_security_results[1].evidence_id: is duplicated"},
		{name: "unknown evidence", mutate: func(response *SemanticAssessmentResponse) { response.EvidenceSecurityResults[0].EvidenceID = "missing" }, want: "evidence_security_results[0].evidence_id: is unknown"},
		{name: "unsupported decision", mutate: func(response *SemanticAssessmentResponse) { response.EvidenceSecurityResults[0].Decision = "unknown" }, want: "evidence_security_results[0].decision: is unsupported"},
		{name: "reject requires signal", mutate: func(response *SemanticAssessmentResponse) { response.EvidenceSecurityResults[0].Decision = "reject" }, want: "reject requires at least one security signal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := semanticAssessmentTestResponse()
			test.mutate(&response)
			_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
			if len(validationErrors) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(validationErrors), test.want) {
				t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v, want %q", validationErrors, test.want)
			}
		})
	}

	response := semanticAssessmentTestResponse()
	startRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 0)
	endRef, _ := SemanticAssessmentBoundaryRef(prepared.Evidence[0], 4)
	response.EvidenceSecurityResults[0].Signals = []SemanticAssessmentSecuritySignal{{EvidenceID: "ev-1", Kind: "instruction_override", StartRef: startRef, EndRef: endRef}}
	response.EvidenceSecurityResults[0].Decision = "reject"
	_, validationErrors := PrepareSemanticAssessmentResponse(prepared, response, limits)
	require.Empty(t, validationErrors)
}
