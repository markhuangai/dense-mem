package domain

import "time"

// ClaimModality represents the epistemic modality of a claim.
//
// Canonical values per AC-8: assertion | question | proposal | speculation | quoted.
type ClaimModality string

const (
	// ModalityAssertion is a direct factual statement.
	ModalityAssertion ClaimModality = "assertion"
	// ModalityQuestion is an interrogative claim seeking information.
	ModalityQuestion ClaimModality = "question"
	// ModalityProposal is a suggestion or recommended course of action.
	ModalityProposal ClaimModality = "proposal"
	// ModalitySpeculation is a speculative or conditional statement.
	ModalitySpeculation ClaimModality = "speculation"
	// ModalityQuoted is a direct quotation attributed to a speaker.
	ModalityQuoted ClaimModality = "quoted"
)

// IsValid reports whether m is a recognised ClaimModality value.
func (m ClaimModality) IsValid() bool {
	switch m {
	case ModalityAssertion, ModalityQuestion, ModalityProposal, ModalitySpeculation, ModalityQuoted:
		return true
	}
	return false
}

// ClaimPolarity represents whether a claim is affirmative (+) or negating (-).
//
// Canonical values per AC-8: '+' and '-'.
type ClaimPolarity string

const (
	// PolarityPlus indicates an affirmative claim.
	PolarityPlus ClaimPolarity = "+"
	// PolarityMinus indicates a negating or contradicting claim.
	PolarityMinus ClaimPolarity = "-"

	// PolarityPositive is a deprecated alias for PolarityPlus. Retained for
	// external compile-time compatibility; new code should use PolarityPlus.
	PolarityPositive ClaimPolarity = PolarityPlus
	// PolarityNegative is a deprecated alias for PolarityMinus. Retained for
	// external compile-time compatibility; new code should use PolarityMinus.
	PolarityNegative ClaimPolarity = PolarityMinus
)

// IsValid reports whether p is a recognised ClaimPolarity value.
func (p ClaimPolarity) IsValid() bool {
	switch p {
	case PolarityPlus, PolarityMinus:
		return true
	}
	return false
}

// ClaimStatus represents the lifecycle state of a claim.
type ClaimStatus string

const (
	// StatusCandidate is a newly extracted claim awaiting verification.
	StatusCandidate ClaimStatus = "candidate"
	// StatusValidated is a claim that passed entailment verification.
	StatusValidated ClaimStatus = "validated"
	// StatusRejected is a claim that failed entailment verification.
	StatusRejected ClaimStatus = "rejected"
	// StatusSuperseded is a claim replaced by a newer version.
	StatusSuperseded ClaimStatus = "superseded"
	// StatusDisputed is a claim contradicted by comparable evidence (Phase 4,
	// AC-27: comparable-contradiction outcome sets status to disputed).
	StatusDisputed ClaimStatus = "disputed"
	// StatusPromoted is a claim that has been successfully promoted to a Fact
	// (Phase 4: c.status = 'promoted' after fact creation).
	StatusPromoted ClaimStatus = "promoted"
)

// IsValid reports whether s is a recognised ClaimStatus value.
func (s ClaimStatus) IsValid() bool {
	switch s {
	case StatusCandidate, StatusValidated, StatusRejected, StatusSuperseded,
		StatusDisputed, StatusPromoted:
		return true
	}
	return false
}

// EntailmentVerdict represents the outcome of entailment verification.
type EntailmentVerdict string

const (
	// VerdictEntailed means the claim is supported by the evidence.
	VerdictEntailed EntailmentVerdict = "entailed"
	// VerdictContradicted means the claim conflicts with the evidence.
	VerdictContradicted EntailmentVerdict = "contradicted"
	// VerdictNeutral means the evidence neither supports nor contradicts the claim.
	VerdictNeutral EntailmentVerdict = "neutral"
	// VerdictInsufficient means evidence is insufficient to reach a verdict.
	// This is the default verdict on claim creation (AC-13) and the fallback
	// when the verifier returns a malformed response or times out (plan R5).
	VerdictInsufficient EntailmentVerdict = "insufficient"

	// VerdictUnverified is a deprecated alias for VerdictInsufficient. Retained
	// for external compile-time compatibility; new code should use
	// VerdictInsufficient.
	VerdictUnverified EntailmentVerdict = VerdictInsufficient
)

// IsValid reports whether v is a recognised EntailmentVerdict value.
func (v EntailmentVerdict) IsValid() bool {
	switch v {
	case VerdictEntailed, VerdictContradicted, VerdictNeutral, VerdictInsufficient:
		return true
	}
	return false
}

// Claim is the full runtime domain model for an extracted knowledge claim.
//
// Invariant: every Claim MUST carry its ProfileID so that all downstream
// repository and graph queries can enforce profile-level isolation. Never
// omit or zero-out ProfileID when constructing a Claim.
type Claim struct {
	// Identity / scope
	ClaimID              string `json:"claim_id"`
	ProfileID            string `json:"team_id"`
	OwnerProfileID       string `json:"owner_profile_id,omitempty"`
	OwnerProfileName     string `json:"owner_profile_name,omitempty"`
	CreatedByProfileID   string `json:"created_by_profile_id,omitempty"`
	CreatedByProfileName string `json:"created_by_profile_name,omitempty"`

	// Semantic triple
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`

	// Linguistic metadata
	Modality  ClaimModality `json:"modality"`
	Polarity  ClaimPolarity `json:"polarity"`
	Speaker   string        `json:"speaker,omitempty"`
	SpanStart int           `json:"span_start"`
	SpanEnd   int           `json:"span_end"`

	// Temporal validity
	ValidFrom  *time.Time `json:"valid_from,omitempty"`
	ValidTo    *time.Time `json:"valid_to,omitempty"`
	RecordedAt time.Time  `json:"recorded_at"`
	RecordedTo *time.Time `json:"recorded_to,omitempty"`

	// Quality signals
	ExtractConf    float64 `json:"extract_conf"`
	ResolutionConf float64 `json:"resolution_conf"`
	SourceQuality  float64 `json:"source_quality"`

	// Verification
	EntailmentVerdict    EntailmentVerdict `json:"entailment_verdict"`
	Status               ClaimStatus       `json:"status"`
	LastVerifierResponse string            `json:"last_verifier_response,omitempty"`
	VerifiedAt           *time.Time        `json:"verified_at,omitempty"`

	// Provenance
	ExtractionModel   string `json:"extraction_model"`
	ExtractionVersion string `json:"extraction_version"`
	VerifierModel     string `json:"verifier_model,omitempty"`
	PipelineRunID     string `json:"pipeline_run_id,omitempty"`

	// Idempotency
	ContentHash    string `json:"content_hash"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// Classification
	// Classification must be a structured map, not a string blob (AC-7).
	Classification               map[string]any `json:"classification,omitempty"`
	ClassificationLatticeVersion string         `json:"classification_lattice_version,omitempty"`
	RecallText                   string         `json:"recall_text,omitempty"`
	IdentifierTokens             []string       `json:"identifier_tokens,omitempty"`

	// Graph relationships: IDs of SourceFragments that support this claim.
	SupportedBy []string   `json:"supported_by,omitempty"`
	Evidence    []Evidence `json:"evidence,omitempty"`
}
