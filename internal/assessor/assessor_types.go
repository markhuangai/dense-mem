package assessor

// SemanticAssessmentRequest is the server-owned, single-call assessor input.
// Team and owner fields are retained for deterministic validation but never
// leave the service boundary.
type SemanticAssessmentRequest struct {
	RequestID                     string                                                `json:"request_id"`
	TeamID                        string                                                `json:"-"`
	OwnerProfileID                string                                                `json:"-"`
	Evidence                      []SemanticReviewEvidence                              `json:"evidence"`
	KnownEvidence                 []SemanticReviewEvidence                              `json:"known_evidence,omitempty"`
	EvidenceEquivalenceCandidates []SemanticAssessmentEvidenceEquivalenceCandidateGroup `json:"evidence_equivalence_candidates,omitempty"`
	ClientProposal                map[string]any                                        `json:"client_proposal,omitempty"`
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
	// Candidate context omission counts are server-side diagnostics. They are
	// intentionally excluded from the provider payload and public response.
	CandidateContextOmittedCandidates       int `json:"-"`
	CandidateContextOmittedPredicateOptions int `json:"-"`
	InputTokens                             int `json:"-"`
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
	RequestID                  string                                        `json:"request_id"`
	EvidenceSecurityResults    []SemanticAssessmentEvidenceSecurityResult    `json:"evidence_security_results"`
	EvidenceEquivalenceResults []SemanticAssessmentEvidenceEquivalenceResult `json:"evidence_equivalence_results"`
	EvidenceConflictResults    []SemanticAssessmentEvidenceConflictResult    `json:"evidence_conflict_results"`
	// SecuritySignals and SecurityResults are in-process compatibility aliases
	// for callers that construct responses directly. They are never serialized.
	SecuritySignals     []SemanticAssessmentSecuritySignal     `json:"-"`
	SecurityResults     []SemanticAssessmentSecurityResult     `json:"-"`
	EntityResults       []SemanticAssessmentEntityResult       `json:"entity_results"`
	RelationshipResults []SemanticAssessmentRelationshipResult `json:"relationship_results"`
	OutputTokens        int                                    `json:"-"`
	InputTokens         int                                    `json:"-"`
	ProviderTurns       int                                    `json:"-"`
}

// SemanticAssessmentEvidenceEquivalenceCandidateGroup is a server-authorized
// allowlist for one submitted evidence item. Candidate content is read-only
// context; the assessor cannot discover additional evidence.
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

// SemanticAssessmentEvidenceEquivalenceResult is the closed new/reuse result
// for one non-exact submitted evidence item.
type SemanticAssessmentEvidenceEquivalenceResult struct {
	EvidenceID          string  `json:"evidence_id"`
	Action              string  `json:"action"`
	CandidateEvidenceID *string `json:"candidate_evidence_id"`
}

// SemanticAssessmentEvidenceConflictResult is one assessor-cited set of
// opposing evidence spans. The server resolves the request-local references
// into rune offsets before any durable state is considered.
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

// SemanticAssessmentSecurityResult remains an in-process alias for the
// evidence-scoped security result type.
type SemanticAssessmentSecurityResult = SemanticAssessmentEvidenceSecurityResult

type SemanticAssessmentEntityResult struct {
	Ref               string  `json:"ref"`
	GroundingRef      *string `json:"grounding_ref"`
	AnchorRef         *string `json:"anchor_ref,omitempty"`
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

type SemanticAssessmentSecuritySignal struct {
	EvidenceID string `json:"-"`
	Kind       string `json:"kind"`
	StartRef   string `json:"start_ref"`
	EndRef     string `json:"end_ref"`
	Start      int    `json:"-"`
	End        int    `json:"-"`
}

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
	EntityCandidateGroups         []SemanticAssessmentEntityCandidateGroup              `json:"entity_candidate_groups"`
	PredicateOptions              []SemanticAssessmentPredicateOption                   `json:"predicate_options"`
	EvidenceEquivalenceCandidates []SemanticAssessmentEvidenceEquivalenceCandidateGroup `json:"evidence_equivalence_candidates"`
}
