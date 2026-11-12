package assessor

import "github.com/markhuangai/dense-mem/internal/modelprovider"

type SemanticAssessmentRequest struct {
	RequestID                     string                                                `json:"request_id"`
	TeamID                        string                                                `json:"-"`
	OwnerProfileID                string                                                `json:"-"`
	Evidence                      []SemanticReviewEvidence                              `json:"evidence"`
	KnownEvidence                 []SemanticReviewEvidence                              `json:"known_evidence,omitempty"`
	EvidenceEquivalenceCandidates []SemanticAssessmentEvidenceEquivalenceCandidateGroup `json:"evidence_equivalence_candidates,omitempty"`
	CandidateContextTokens        int                                                   `json:"candidate_context_tokens"`
	CandidateContextTruncated     bool                                                  `json:"candidate_context_truncated"`
	// Candidate context omission counts are server-side diagnostics. They are
	// intentionally excluded from the provider payload and public response.
	CandidateContextOmittedCandidates int                        `json:"-"`
	InputTokens                       int                        `json:"-"`
	DecisionRouting                   *SemanticAssessmentRouting `json:"-"`
}

type SemanticAssessmentResponse struct {
	DecisionStages             []modelprovider.DecisionStage                 `json:"decision_stages,omitempty"`
	RequestID                  string                                        `json:"request_id"`
	EvidenceSecurityResults    []SemanticAssessmentEvidenceSecurityResult    `json:"evidence_security_results"`
	EvidenceEquivalenceResults []SemanticAssessmentEvidenceEquivalenceResult `json:"evidence_equivalence_results"`
	EvidenceConflictResults    []SemanticAssessmentEvidenceConflictResult    `json:"evidence_conflict_results"`
	// SecuritySignals and SecurityResults are in-process compatibility aliases
	// for callers that construct responses directly. They are never serialized.
	SecuritySignals []SemanticAssessmentSecuritySignal `json:"-"`
	SecurityResults []SemanticAssessmentSecurityResult `json:"-"`
	OutputTokens    int                                `json:"-"`
	InputTokens     int                                `json:"-"`
	ProviderTurns   int                                `json:"-"`
}

type SemanticAssessmentEvidenceEquivalenceCandidateGroup struct {
	EvidenceID string                                           `json:"evidence_id"`
	Candidates []SemanticAssessmentEvidenceEquivalenceCandidate `json:"candidates"`
}

type SemanticAssessmentEvidenceEquivalenceCandidate struct {
	EvidenceID     string         `json:"evidence_id"`
	Content        string         `json:"content"`
	BoundaryText   string         `json:"boundary_text,omitempty"`
	BoundaryRefs   map[string]int `json:"-"`
	BoundaryPrefix string         `json:"-"`
}

type SemanticAssessmentEvidenceEquivalenceResult struct {
	EvidenceID          string  `json:"evidence_id"`
	Action              string  `json:"action"`
	CandidateEvidenceID *string `json:"candidate_evidence_id"`
}

type SemanticAssessmentEvidenceConflictResult struct {
	Positions []SemanticAssessmentEvidenceConflictPosition `json:"positions"`
}

type SemanticAssessmentEvidenceConflictPosition struct {
	EvidenceID string `json:"evidence_id"`
	StartRef   string `json:"start_ref"`
	EndRef     string `json:"end_ref"`
	Start      int    `json:"-"`
	End        int    `json:"-"`
}

type SemanticAssessmentEvidenceSecurityResult struct {
	EvidenceID string                             `json:"evidence_id"`
	Decision   string                             `json:"decision"`
	Signals    []SemanticAssessmentSecuritySignal `json:"signals"`
}

type SemanticAssessmentGroundedRange struct {
	EvidenceID string `json:"evidence_id"`
	StartRef   string `json:"start_ref"`
	EndRef     string `json:"end_ref"`
	Start      int    `json:"-"`
	End        int    `json:"-"`
}

type SemanticAssessmentSecuritySignal struct {
	EvidenceID string `json:"-"`
	Kind       string `json:"kind"`
	StartRef   string `json:"start_ref"`
	EndRef     string `json:"end_ref"`
	Start      int    `json:"-"`
	End        int    `json:"-"`
}

type SemanticAssessmentSecurityResult = SemanticAssessmentEvidenceSecurityResult

type semanticAssessmentCandidateContext struct {
	EvidenceEquivalenceCandidates []SemanticAssessmentEvidenceEquivalenceCandidateGroup `json:"evidence_equivalence_candidates,omitempty"`
}
