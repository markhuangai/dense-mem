package assessor

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyUnitAssessmentRequiresOneCompleteEvidenceResponse(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	request, issues := PrepareSemanticAssessmentRequest(SemanticAssessmentRequest{RequestID: "request", TeamID: "team", Evidence: []SemanticReviewEvidence{{EvidenceID: "submitted", Content: "The active timeout is 30 seconds."}}, EvidenceEquivalenceCandidates: []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "submitted", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "known", Content: "The active timeout is 30 seconds."}}}}}, limits)
	require.Empty(t, issues)
	original := `{"request_id":"request","evidence_security_results":[{"evidence_id":"submitted","decision":"pass","signals":[]}],"evidence_equivalence_results":[{"evidence_id":"submitted","action":"reuse","candidate_evidence_id":"known"}],"evidence_conflict_results":[]}`
	response, err := DecodeSemanticAssessmentResponseJSON([]byte(original), limits)
	require.NoError(t, err)
	_, issues = PrepareSemanticAssessmentResponse(request, response, limits)
	require.Empty(t, issues)
	for _, mutate := range []func(*SemanticAssessmentResponse){
		func(r *SemanticAssessmentResponse) { r.EvidenceSecurityResults = nil },
		func(r *SemanticAssessmentResponse) {
			r.EvidenceSecurityResults = append(r.EvidenceSecurityResults, r.EvidenceSecurityResults[0])
		},
		func(r *SemanticAssessmentResponse) { r.EvidenceSecurityResults[0].EvidenceID = "foreign" },
		func(r *SemanticAssessmentResponse) { r.EvidenceEquivalenceResults = nil },
		func(r *SemanticAssessmentResponse) {
			id := "outside-allowlist"
			r.EvidenceEquivalenceResults[0].CandidateEvidenceID = &id
		},
	} {
		var candidate SemanticAssessmentResponse
		require.NoError(t, json.Unmarshal([]byte(original), &candidate))
		mutate(&candidate)
		_, issues := PrepareSemanticAssessmentResponse(request, candidate, limits)
		require.NotEmpty(t, issues)
	}
}

func TestEvidenceOnlyUnitAssessmentRejectsRetiredAndUnknownFields(t *testing.T) {
	original := map[string]any{"request_id": "request", "evidence_security_results": []any{}, "evidence_equivalence_results": []any{}, "evidence_conflict_results": []any{}}
	for _, field := range []string{"entity_results", "relationship_results", "predicate_registrations", "unknown"} {
		t.Run(field, func(t *testing.T) {
			candidate := map[string]any{}
			for k, v := range original {
				candidate[k] = v
			}
			candidate[field] = []any{}
			raw, err := json.Marshal(candidate)
			require.NoError(t, err)
			_, err = DecodeSemanticAssessmentResponseJSON(raw, DefaultSemanticAssessmentLimits())
			require.Error(t, err)
		})
	}
	_, err := DecodeSemanticAssessmentResponseJSON([]byte(`{"request_id":"request","request_id":"other","evidence_security_results":[],"evidence_equivalence_results":[],"evidence_conflict_results":[]}`), DefaultSemanticAssessmentLimits())
	require.Error(t, err)
}
