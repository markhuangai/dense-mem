package verifier

import "github.com/markhuangai/dense-mem/internal/assessor"

// SemanticAssessmentRequest is the server-owned, single-call assessor input.
// Team and owner fields are retained for deterministic validation but never
// leave the service boundary.
type SemanticAssessmentRequest struct {
	RequestID      string                   `json:"request_id"`
	TeamID         string                   `json:"-"`
	OwnerProfileID string                   `json:"-"`
	Evidence       []SemanticReviewEvidence `json:"evidence"`
	ClientProposal map[string]any           `json:"client_proposal,omitempty"`
	// EntityCandidateGroups are reuse allowlists for spans the assessor may
	// extract, not required output targets.
	EntityCandidateGroups     []SemanticAssessmentEntityCandidateGroup    `json:"entity_candidate_groups"`
	PredicateOptions          []SemanticAssessmentPredicateOption         `json:"predicate_options"`
	RequiredRelationshipRefs  []SemanticAssessmentRequiredRelationshipRef `json:"-"`
	SubmittedEntities         []SemanticAssessmentSubmittedEntity         `json:"submitted_entities,omitempty"`
	SubmittedRelationships    []SemanticAssessmentSubmittedRelationship   `json:"submitted_relationships,omitempty"`
	SubmissionContract        *SemanticAssessmentSubmissionContract       `json:"-"`
	CandidateContextTokens    int                                         `json:"candidate_context_tokens"`
	CandidateContextTruncated bool                                        `json:"candidate_context_truncated"`
	InputTokens               int                                         `json:"-"`
}

type SemanticAssessmentEntityCandidateGroup struct {
	Surface                   string                              `json:"surface"`
	EvidenceID                string                              `json:"evidence_id"`
	GroundingRef              string                              `json:"grounding_ref"`
	Start                     int                                 `json:"-"`
	End                       int                                 `json:"-"`
	CandidateContextTruncated bool                                `json:"candidate_context_truncated"`
	Candidates                []SemanticAssessmentEntityCandidate `json:"candidates"`
}

type SemanticAssessmentEntityCandidate struct {
	EntityID        string         `json:"entity_id"`
	CanonicalName   string         `json:"canonical_name"`
	Kind            string         `json:"kind"`
	IdentityContext map[string]any `json:"identity_context"`
}

type SemanticAssessmentPredicateOption struct {
	PredicateKey        string   `json:"predicate_key"`
	Version             int      `json:"version"`
	Aliases             []string `json:"aliases"`
	AllowedSubjectKinds []string `json:"allowed_subject_kinds"`
	AllowedObjectKinds  []string `json:"allowed_object_kinds"`
	RelationshipKind    string   `json:"relationship_kind"`
	CurrentCardinality  string   `json:"current_cardinality"`
}

type SemanticAssessmentResponse struct {
	RequestID           string                                 `json:"request_id"`
	SecuritySignals     []SemanticAssessmentSecuritySignal     `json:"security_signals"`
	SecurityResults     []SemanticAssessmentSecurityResult     `json:"security_results"`
	EntityResults       []SemanticAssessmentEntityResult       `json:"entity_results"`
	RelationshipResults []SemanticAssessmentRelationshipResult `json:"relationship_results"`
	OutputTokens        int                                    `json:"-"`
	InputTokens         int                                    `json:"-"`
	ProviderTurns       int                                    `json:"-"`
}

// Security result fields and validation are owned by the assessor package;
// verifier keeps this alias for its legacy in-process API.
type SemanticAssessmentSecurityResult = assessor.SemanticAssessmentSecurityResult

type SemanticAssessmentEntityResult struct {
	Ref               string  `json:"ref"`
	GroundingRef      *string `json:"grounding_ref"`
	Surface           string  `json:"-"`
	Kind              string  `json:"-"`
	EvidenceID        string  `json:"-"`
	Start             int     `json:"-"`
	End               int     `json:"-"`
	Action            string  `json:"action"`
	CandidateEntityID *string `json:"candidate_entity_id"`
}

type SemanticAssessmentEvidenceSpan struct {
	EvidenceID string `json:"evidence_id"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type SemanticAssessmentGroundedRange struct {
	EvidenceID string `json:"evidence_id"`
	StartRef   string `json:"start_ref"`
	EndRef     string `json:"end_ref"`
	Start      int    `json:"-"`
	End        int    `json:"-"`
}

type SemanticAssessmentSecuritySignal = assessor.SemanticAssessmentSecuritySignal

type SemanticAssessmentValue struct {
	ValueType      string  `json:"value_type"`
	CanonicalValue string  `json:"canonical_value"`
	Display        *string `json:"display"`
	Unit           *string `json:"unit"`
}

type SemanticAssessmentRelationshipResult struct {
	Ref         string                                `json:"ref"`
	Disposition string                                `json:"disposition"`
	Reason      *string                               `json:"reason"`
	Splits      []SemanticAssessmentRelationshipSplit `json:"splits"`
}

type SemanticAssessmentRelationshipSplit struct {
	SplitIndex            int                                      `json:"split_index"`
	SubjectRef            string                                   `json:"subject_ref"`
	OriginalPredicate     string                                   `json:"-"`
	PredicateRange        SemanticAssessmentGroundedRange          `json:"predicate_range"`
	PredicateStatus       string                                   `json:"predicate_status"`
	PredicateKey          *string                                  `json:"predicate_key"`
	PredicateVersion      *int                                     `json:"predicate_version"`
	PredicateRegistration *SemanticAssessmentPredicateRegistration `json:"predicate_registration"`
	ObjectRef             *string                                  `json:"object_ref"`
	ObjectValue           *SemanticAssessmentValue                 `json:"object_value"`
	ValueRange            *SemanticAssessmentGroundedRange         `json:"value_range"`
	Polarity              string                                   `json:"polarity"`
	SupportRanges         []SemanticAssessmentGroundedRange        `json:"support_ranges"`
	Evidence              []SemanticAssessmentEvidenceSpan         `json:"-"`
	ValidFrom             *string                                  `json:"valid_from"`
	ValidTo               *string                                  `json:"valid_to"`
}

type SemanticAssessmentPredicateRegistration struct {
	PredicateKey       string `json:"predicate_key"`
	RelationshipKind   string `json:"relationship_kind"`
	CurrentCardinality string `json:"current_cardinality"`
}

type semanticAssessmentCandidateContext struct {
	EntityCandidateGroups []SemanticAssessmentEntityCandidateGroup `json:"entity_candidate_groups"`
	PredicateOptions      []SemanticAssessmentPredicateOption      `json:"predicate_options"`
}
