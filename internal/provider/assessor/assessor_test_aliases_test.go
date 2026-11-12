package assessorprovider

import (
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type SemanticAssessmentRequest = assessor.SemanticAssessmentRequest
type SemanticAssessmentLimits = assessor.SemanticAssessmentLimits
type SemanticValidationError = assessor.SemanticValidationError
type SemanticAssessmentResponse = assessor.SemanticAssessmentResponse
type SemanticAssessmentRepairRequest = assessor.SemanticAssessmentRepairRequest
type SemanticAssessmentGroundedRange = assessor.SemanticAssessmentGroundedRange
type SemanticAssessmentSecuritySignal = assessor.SemanticAssessmentSecuritySignal
type SemanticAssessmentEvidenceSecurityResult = assessor.SemanticAssessmentEvidenceSecurityResult
type SemanticAssessmentEvidenceEquivalenceResult = assessor.SemanticAssessmentEvidenceEquivalenceResult
type SemanticAssessmentEvidenceConflictResult = assessor.SemanticAssessmentEvidenceConflictResult
type SemanticAssessmentSecurityResult = assessor.SemanticAssessmentSecurityResult
type SemanticReviewEvidence = assessor.SemanticReviewEvidence

const SemanticAssessmentMaxProviderTurns = assessor.SemanticAssessmentMaxProviderTurns

const (
	semanticAssessmentSystemPrompt          = assessor.SemanticAssessmentSystemPrompt
	semanticAssessmentCorrectionInstruction = assessor.SemanticAssessmentCorrectionInstruction
)

var ErrVerifierProvider = modelprovider.ErrVerifierProvider

func PrepareSemanticAssessmentRequest(req SemanticAssessmentRequest, limits SemanticAssessmentLimits) (SemanticAssessmentRequest, []assessor.SemanticValidationError) {
	return assessor.PrepareSemanticAssessmentRequest(req, limits)
}

func PrepareSemanticAssessmentEvidence(evidence SemanticReviewEvidence) SemanticReviewEvidence {
	return assessor.PrepareSemanticAssessmentEvidence(evidence)
}

func SemanticAssessmentBoundaryRef(evidence SemanticReviewEvidence, offset int) (string, bool) {
	return assessor.SemanticAssessmentBoundaryRef(evidence, offset)
}

func SemanticAssessmentResponseSchema() map[string]any {
	return assessor.SemanticAssessmentResponseSchema()
}

func DecodeSemanticAssessmentResponseJSON(raw []byte, limits SemanticAssessmentLimits) (SemanticAssessmentResponse, error) {
	return assessor.DecodeSemanticAssessmentResponseJSON(raw, limits)
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return encoded
}

func newTestVerifierConfig(serverURL, apiKey, model string) *config.Config {
	return &config.Config{AIAPIURL: serverURL, AIAPIKey: apiKey, AIVerifierModel: model}
}

func semanticAssessmentTestRange(evidence SemanticReviewEvidence, start, end int) SemanticAssessmentGroundedRange {
	startRef, startOK := SemanticAssessmentBoundaryRef(evidence, start)
	endRef, endOK := SemanticAssessmentBoundaryRef(evidence, end)
	if !startOK || !endOK {
		panic("semantic assessment test boundary is missing")
	}
	return SemanticAssessmentGroundedRange{EvidenceID: evidence.EvidenceID, StartRef: startRef, EndRef: endRef, Start: start, End: end}
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

func stringPointer(value string) *string { return &value }
