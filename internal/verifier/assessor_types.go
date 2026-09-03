package verifier

import "github.com/markhuangai/dense-mem/internal/assessor"

// Semantic assessment wire and request types are owned by assessor. These
// aliases keep the legacy verifier entry point on the same closed contract.
type SemanticAssessmentRequest = assessor.SemanticAssessmentRequest
type SemanticAssessmentEntityCandidateGroup = assessor.SemanticAssessmentEntityCandidateGroup
type SemanticAssessmentEntityCandidate = assessor.SemanticAssessmentEntityCandidate
type SemanticAssessmentPredicateOption = assessor.SemanticAssessmentPredicateOption
type SemanticAssessmentResponse = assessor.SemanticAssessmentResponse
type SemanticAssessmentEntityResult = assessor.SemanticAssessmentEntityResult
type SemanticAssessmentEvidenceSpan = assessor.SemanticAssessmentEvidenceSpan
type SemanticAssessmentGroundedRange = assessor.SemanticAssessmentGroundedRange
type SemanticAssessmentSecuritySignal = assessor.SemanticAssessmentSecuritySignal
type SemanticAssessmentEvidenceSecurityResult = assessor.SemanticAssessmentEvidenceSecurityResult
type SemanticAssessmentEvidenceEquivalenceResult = assessor.SemanticAssessmentEvidenceEquivalenceResult
type SemanticAssessmentEvidenceConflictResult = assessor.SemanticAssessmentEvidenceConflictResult
type SemanticAssessmentEvidenceConflictPosition = assessor.SemanticAssessmentEvidenceConflictPosition
type SemanticAssessmentSecurityResult = assessor.SemanticAssessmentSecurityResult
type SemanticAssessmentValue = assessor.SemanticAssessmentValue
type SemanticAssessmentRelationshipResult = assessor.SemanticAssessmentRelationshipResult
type SemanticAssessmentRelationshipSplit = assessor.SemanticAssessmentRelationshipSplit
type SemanticAssessmentPredicateRegistration = assessor.SemanticAssessmentPredicateRegistration
type SemanticAssessmentRequiredRelationshipRef = assessor.SemanticAssessmentRequiredRelationshipRef
type SemanticAssessmentSubmissionContract = assessor.SemanticAssessmentSubmissionContract
type SemanticAssessmentRequiredEntityRef = assessor.SemanticAssessmentRequiredEntityRef
type SemanticAssessmentEntityGrounding = assessor.SemanticAssessmentEntityGrounding
type SemanticAssessmentSubmittedEntity = assessor.SemanticAssessmentSubmittedEntity
type SemanticAssessmentSubmittedRelationship = assessor.SemanticAssessmentSubmittedRelationship

type semanticAssessmentCandidateContext struct {
	EntityCandidateGroups         []SemanticAssessmentEntityCandidateGroup                       `json:"entity_candidate_groups"`
	PredicateOptions              []SemanticAssessmentPredicateOption                            `json:"predicate_options"`
	EvidenceEquivalenceCandidates []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup `json:"evidence_equivalence_candidates"`
}
