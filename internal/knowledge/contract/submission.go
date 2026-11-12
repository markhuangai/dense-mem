package contract

import (
	"encoding/json"
	"time"
)

// RememberCommitScope identifies the authenticated request being committed.
// It intentionally contains no placement run, worker, lease, or retry fields.
type RememberCommitScope struct {
	TeamID         string
	OwnerProfileID string
	IngestID       string
}

type SubmissionAssessment struct {
	TeamID                            string
	AssessmentID                      string
	OwnerProfileID                    string
	IngestID                          string
	RequestID                         string
	AssessorContractVersion           string
	Model                             string
	Tokenizer                         string
	RevisionNumber                    int
	ProviderTurns                     int
	InputTokens                       int
	OutputTokens                      int
	CandidateContextTokens            int
	CandidateContextTruncated         bool
	CandidateContextOmittedCandidates int
	NormalizedResponse                json.RawMessage
	ResponseHash                      string
	ValidatedAt                       time.Time
	CreatedAt                         time.Time
}

type SubmissionAssessmentItemInput struct {
	FragmentID string
	EvidenceID string
}

// EvidenceConflictPositionInput is a validated assessor citation. EvidenceID
// is a request-local submitted ID, an explicit known evidence ID, or an
// allowlisted existing candidate fragment ID. Start and End are Unicode-rune
// offsets resolved by the assessor boundary validator.
type EvidenceConflictPositionInput struct {
	EvidenceID string
	Start      int
	End        int
}

type EvidenceConflictResultInput struct {
	Positions []EvidenceConflictPositionInput
}

type CommitSubmissionAssessmentInput struct {
	RememberCommitScope
	AssessmentID            string
	Items                   []SubmissionAssessmentItemInput
	KnownEvidenceSnapshot   []SubmissionAssessmentKnownEvidence
	EvidenceConflictResults []EvidenceConflictResultInput
	// EvidenceConflictCandidateEvidenceIDs is the immutable server-side
	// allowlist used to reauthorize assessor citations to T05 candidates.
	// Keys are submitted request evidence IDs and values are candidate fragment
	// IDs returned by the bounded duplicate-candidate lookup.
	EvidenceConflictCandidateEvidenceIDs map[string][]string
	Payload                              map[string]any
}

// EvidenceSecurityResult is the assessor's complete security disposition for
// one submitted evidence fragment. Accepted commits require exactly one safe
// result per fragment; unsafe results fail the whole Remember batch before any
// semantic or search write.
type EvidenceSecurityResult struct {
	FragmentID    string
	EvidenceID    string
	EvidenceIndex int
	Decision      string
	Safe          bool
	Signals       []SecuritySignalInput
}

// SynchronousRememberCommitInput contains all request-owned state needed to make
// one final Remember transaction. Provider work happens before this input is
// committed; no field represents a pre-provider reservation.
type SynchronousRememberCommitInput struct {
	AssessorDiagnostics map[string]any
	TeamID              string
	OwnerProfileID      string
	IngestID            string
	SpaceID             string
	SpaceGeneration     int64
	IdempotencyKey      string
	RequestHash         string
	SourceSummary       string

	Metadata                          map[string]any
	Evidence                          []EvidenceInput
	AssessmentID                      string
	AssessmentJSON                    json.RawMessage
	EvidenceSecurityResults           []EvidenceSecurityResult
	ProviderTurns                     int
	InputTokens                       int
	OutputTokens                      int
	CandidateContextOmittedCandidates int
	AssessorTurns                     int
	Duration                          time.Duration
	StartedAt                         time.Time
	CorrelationID                     string
	PublicResult                      map[string]any
	DuplicateResolutions              []RememberDuplicateResolution
	Commit                            CommitSubmissionAssessmentInput
}

type CommitSubmissionAssessmentResult struct {
	Status          string
	OutcomeIDs      []string
	SearchDocuments []SearchDocumentResult
}

type SubmissionAssessmentSecurityQuarantineInput struct {
	FragmentID string
	SecurityEventDraft
}
