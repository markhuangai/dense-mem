package assessor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticAssessmentPrepareAndValidateCompleteResponse(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	if prepared.CandidateContextTokens <= 0 || prepared.InputTokens <= prepared.CandidateContextTokens {
		t.Fatalf("prepared token counts = input:%d candidate:%d", prepared.InputTokens, prepared.CandidateContextTokens)
	}

	raw, err := json.Marshal(semanticAssessmentTestResponse())
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	decoded, err := DecodeSemanticAssessmentResponseJSON(raw, limits)
	if err != nil {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v", err)
	}
	validated, errs := PrepareSemanticAssessmentResponse(prepared, decoded, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentResponse() errors = %#v", errs)
	}
	if validated.OutputTokens <= 0 {
		t.Fatalf("OutputTokens = %d, want positive", validated.OutputTokens)
	}
}

func TestSemanticAssessmentWireRejectsObsoleteAssessorFields(t *testing.T) {
	_, limits := semanticAssessmentTestRequest(t)
	base, err := json.Marshal(semanticAssessmentTestResponse())
	require.NoError(t, err)

	for _, field := range []string{"confidence", "rationale", "modality", "scope_status", "scope_key", "evidence_verdict", "temporal_verdict"} {
		t.Run(field, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, json.Unmarshal(base, &payload))
			payload[field] = "obsolete"
			encoded, marshalErr := json.Marshal(payload)
			require.NoError(t, marshalErr)
			_, decodeErr := DecodeSemanticAssessmentResponseJSON(encoded, limits)
			require.Error(t, decodeErr)
			require.Contains(t, decodeErr.Error(), field+": is unknown")
		})
	}
}

func TestSemanticAssessmentWireRejectsDuplicateFields(t *testing.T) {
	_, limits := semanticAssessmentTestRequest(t)
	_, err := DecodeSemanticAssessmentResponseJSON([]byte(`{"request_id":"a","evidence_security_results":[],"evidence_equivalence_results":[],"evidence_conflict_results":[],"entity_results":[],"relationship_results":[],"request_id":"b"}`), limits)
	require.ErrorContains(t, err, "duplicate JSON field")
}

func TestSemanticAssessmentRejectsWholeResponseWhenRequiredFieldIsMissing(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := semanticAssessmentTestResponse()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	security := payload["evidence_security_results"].([]any)
	delete(security[0].(map[string]any), "decision")
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal malformed response: %v", err)
	}
	if _, err := DecodeSemanticAssessmentResponseJSON(encoded, limits); err == nil || !strings.Contains(err.Error(), "decision") {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want missing decision", err)
	}
	_ = prepared
}

func TestSemanticAssessmentTokenBudgetsAreEnforced(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	req.EvidenceEquivalenceCandidates = []SemanticAssessmentEvidenceEquivalenceCandidateGroup{{EvidenceID: "ev-1", Candidates: []SemanticAssessmentEvidenceEquivalenceCandidate{}}}
	limits.MaxCandidateContextTokens = 1
	if _, errs := PrepareSemanticAssessmentRequest(req, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "candidate_context_tokens") {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want candidate token budget error", errs)
	}

	limits = DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 1
	raw, err := json.Marshal(semanticAssessmentTestResponse())
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if _, err := DecodeSemanticAssessmentResponseJSON(raw, limits); err == nil || !strings.Contains(err.Error(), "token limit") {
		t.Fatalf("DecodeSemanticAssessmentResponseJSON() error = %v, want output token budget error", err)
	}
}

func TestSemanticAssessmentInputBudgetIncludesFixedSystemPrompt(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		t.Fatalf("marshal prepared request: %v", err)
	}
	payloadTokens, err := CountTokens(string(payload), limits.Tokenizer)
	if err != nil {
		t.Fatalf("CountTokens(payload) error = %v", err)
	}
	if prepared.InputTokens <= payloadTokens {
		t.Fatalf("InputTokens = %d, want prompt-inclusive count greater than payload %d", prepared.InputTokens, payloadTokens)
	}
	limits.MaxInputTokens = payloadTokens + 1
	if _, errs := PrepareSemanticAssessmentRequest(req, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "input_tokens") {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v, want prompt-inclusive input token budget error", errs)
	}
}

func TestSemanticAssessmentResponseSchemaIsClosed(t *testing.T) {
	schema := SemanticAssessmentResponseSchema()
	assertClosedProviderObjects(t, schema, "assessment response")
	assertOpenAIStrictSchemaSubset(t, schema, "assessment response")
}

func semanticAssessmentTestRequest(t *testing.T) (SemanticAssessmentRequest, SemanticAssessmentLimits) {
	t.Helper()
	return SemanticAssessmentRequest{RequestID: "assess-1", TeamID: "team-1", OwnerProfileID: "owner-1", Evidence: []SemanticReviewEvidence{{EvidenceID: "ev-1", FragmentID: "fragment-1", Content: "Mark works on Dense-Mem."}}}, DefaultSemanticAssessmentLimits()
}

func semanticAssessmentPreparedTestRequest(t *testing.T) (SemanticAssessmentRequest, SemanticAssessmentLimits) {
	t.Helper()
	request, limits := semanticAssessmentTestRequest(t)
	request.Evidence[0] = PrepareSemanticAssessmentEvidence(request.Evidence[0])
	return request, limits
}

func semanticAssessmentTestResponse() SemanticAssessmentResponse {
	return SemanticAssessmentResponse{RequestID: "assess-1", EvidenceSecurityResults: []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "ev-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}}, EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{}, EvidenceConflictResults: []SemanticAssessmentEvidenceConflictResult{}}
}

func semanticAssessmentTestRange(evidence SemanticReviewEvidence, start, end int) SemanticAssessmentGroundedRange {
	startRef, startOK := SemanticAssessmentBoundaryRef(evidence, start)
	endRef, endOK := SemanticAssessmentBoundaryRef(evidence, end)
	if !startOK || !endOK {
		panic("semantic assessment test boundary is missing")
	}
	return SemanticAssessmentGroundedRange{
		EvidenceID: evidence.EvidenceID,
		StartRef:   startRef,
		EndRef:     endRef,
		Start:      start,
		End:        end,
	}
}

func stringPointer(value string) *string {
	return &value
}
