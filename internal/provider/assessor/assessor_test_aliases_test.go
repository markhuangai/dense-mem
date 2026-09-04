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
type SemanticAssessmentEntityCandidateGroup = assessor.SemanticAssessmentEntityCandidateGroup
type SemanticAssessmentEntityCandidate = assessor.SemanticAssessmentEntityCandidate
type SemanticAssessmentRelationshipSplit = assessor.SemanticAssessmentRelationshipSplit
type SemanticAssessmentEvidenceSpan = assessor.SemanticAssessmentEvidenceSpan
type SemanticAssessmentGroundedRange = assessor.SemanticAssessmentGroundedRange
type SemanticAssessmentSecuritySignal = assessor.SemanticAssessmentSecuritySignal
type SemanticAssessmentEvidenceSecurityResult = assessor.SemanticAssessmentEvidenceSecurityResult
type SemanticAssessmentEvidenceEquivalenceResult = assessor.SemanticAssessmentEvidenceEquivalenceResult
type SemanticAssessmentEvidenceConflictResult = assessor.SemanticAssessmentEvidenceConflictResult
type SemanticAssessmentSecurityResult = assessor.SemanticAssessmentSecurityResult
type SemanticAssessmentValue = assessor.SemanticAssessmentValue
type SemanticAssessmentPredicateRegistration = assessor.SemanticAssessmentPredicateRegistration
type SemanticAssessmentRequiredEntityRef = assessor.SemanticAssessmentRequiredEntityRef
type SemanticAssessmentRequiredRelationshipRef = assessor.SemanticAssessmentRequiredRelationshipRef
type SemanticAssessmentEntityGrounding = assessor.SemanticAssessmentEntityGrounding
type SemanticAssessmentSubmissionContract = assessor.SemanticAssessmentSubmissionContract
type SemanticAssessmentRelationshipResult = assessor.SemanticAssessmentRelationshipResult
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
	return SemanticAssessmentRequest{
		RequestID:      "assess-1",
		TeamID:         "team-1",
		OwnerProfileID: "owner-1",
		Evidence:       []SemanticReviewEvidence{{EvidenceID: "ev-1", FragmentID: "fragment-1", Content: "Mark works on Dense-Mem."}},
		EntityCandidateGroups: []SemanticAssessmentEntityCandidateGroup{
			{Surface: "Mark", EvidenceID: "ev-1", GroundingRef: "grounding-mark", Start: 0, End: 4, Candidates: []SemanticAssessmentEntityCandidate{{EntityID: "entity-mark", CanonicalName: "Mark Huang", Kind: "person"}}},
			{Surface: "Dense-Mem", EvidenceID: "ev-1", GroundingRef: "grounding-dense-mem", Start: 14, End: 23, Candidates: []SemanticAssessmentEntityCandidate{{EntityID: "entity-dense-mem", CanonicalName: "Dense-Mem", Kind: "product"}}},
		},
		PredicateOptions: []assessor.SemanticAssessmentPredicateOption{{PredicateKey: "works_on", Version: 1, Aliases: []string{"works on"}, AllowedSubjectKinds: []string{"person"}, AllowedObjectKinds: []string{"product"}, RelationshipKind: "state", CurrentCardinality: "many"}},
	}, DefaultSemanticAssessmentLimits()
}

func semanticAssessmentSubmissionContractTestRequest(t *testing.T) (SemanticAssessmentRequest, SemanticAssessmentLimits) {
	t.Helper()
	request, limits := semanticAssessmentTestRequest(t)
	request.Evidence[0] = PrepareSemanticAssessmentEvidence(request.Evidence[0])
	markStart, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 0)
	markEnd, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 4)
	denseMemStart, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 14)
	denseMemEnd, _ := SemanticAssessmentBoundaryRef(request.Evidence[0], 23)
	productRef := "product-1"
	request.SubmissionContract = &SemanticAssessmentSubmissionContract{
		Entities: []SemanticAssessmentRequiredEntityRef{
			{Ref: "person-1", Name: "Mark", Kind: "person", EvidenceIDs: []string{"ev-1"}, Groundings: []SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-mark", EvidenceID: "ev-1", Surface: "Mark", StartRef: markStart, EndRef: markEnd}}},
			{Ref: "product-1", Name: "Dense-Mem", Kind: "product", EvidenceIDs: []string{"ev-1"}, Groundings: []SemanticAssessmentEntityGrounding{{GroundingRef: "grounding-dense-mem", EvidenceID: "ev-1", Surface: "Dense-Mem", StartRef: denseMemStart, EndRef: denseMemEnd}}},
		},
		Relationships: []SemanticAssessmentRequiredRelationshipRef{{ProposalID: "relationship-1", PredicateHint: "works_on", EvidenceIDs: []string{"ev-1"}, SubjectRef: "person-1", ObjectRef: &productRef, Polarity: "+"}},
	}
	return request, limits
}

func semanticAssessmentTestResponse() SemanticAssessmentResponse {
	mark, denseMem, predicate, version := "entity-mark", "entity-dense-mem", "works_on", 1
	markGrounding, denseMemGrounding := "grounding-mark", "grounding-dense-mem"
	evidence := PrepareSemanticAssessmentEvidence(SemanticReviewEvidence{EvidenceID: "ev-1", Content: "Mark works on Dense-Mem."})
	return SemanticAssessmentResponse{
		RequestID:                  "assess-1",
		EvidenceSecurityResults:    []SemanticAssessmentEvidenceSecurityResult{{EvidenceID: "ev-1", Decision: "pass", Signals: []SemanticAssessmentSecuritySignal{}}},
		EvidenceEquivalenceResults: []SemanticAssessmentEvidenceEquivalenceResult{},
		EvidenceConflictResults:    []assessor.SemanticAssessmentEvidenceConflictResult{},
		EntityResults: []assessor.SemanticAssessmentEntityResult{
			{Ref: "person-1", GroundingRef: &markGrounding, Surface: "Mark", Kind: "person", EvidenceID: "ev-1", Start: 0, End: 4, Action: "reuse", CandidateEntityID: &mark},
			{Ref: "product-1", GroundingRef: &denseMemGrounding, Surface: "Dense-Mem", Kind: "product", EvidenceID: "ev-1", Start: 14, End: 23, Action: "reuse", CandidateEntityID: &denseMem},
		},
		RelationshipResults: []SemanticAssessmentRelationshipResult{{Ref: "relationship-1", Disposition: "stored", Splits: []SemanticAssessmentRelationshipSplit{{SplitIndex: 0, SubjectRef: "person-1", OriginalPredicate: "works on", PredicateRange: semanticAssessmentTestRange(evidence, 5, 13), PredicateStatus: "resolved", PredicateKey: &predicate, PredicateVersion: &version, ObjectRef: stringPointer("product-1"), Polarity: "+", Evidence: []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 24}}, SupportRanges: []SemanticAssessmentGroundedRange{semanticAssessmentTestRange(evidence, 0, 24)}}}}},
	}
}

func stringPointer(value string) *string { return &value }
