package repository

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
	TeamID                    string
	AssessmentID              string
	OwnerProfileID            string
	IngestID                  string
	RequestID                 string
	AssessorContractVersion   string
	Model                     string
	Tokenizer                 string
	RevisionNumber            int
	ProviderTurns             int
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
	CreatedAt                 time.Time
}

type SubmissionAssessmentItemInput struct {
	FragmentID string
}

type SubmissionAssessmentEntityResolutionInput struct {
	Resolution SemanticEntityResolutionInput
}

type SubmissionAssessmentRelationshipObservationInput struct {
	RelationshipRef string
	SplitIndex      int
	Observation     SemanticRelationshipDecisionInput
}

type SubmissionRelationshipSplitInput struct {
	SplitIndex          int    `json:"split_index"`
	RelationshipID      string `json:"relationship_id"`
	RelationshipVersion int    `json:"relationship_version"`
	Status              string `json:"status"`
}

// SubmissionRelationshipResult is the durable disposition of one submitted
// relationship ref, independent of whether an active semantic record exists.
type SubmissionRelationshipResult struct {
	RelationshipRef string                             `json:"ref"`
	Disposition     string                             `json:"disposition"`
	Reason          string                             `json:"reason,omitempty"`
	Splits          []SubmissionRelationshipSplitInput `json:"splits"`
}

type SubmissionRelationshipResultInput struct {
	RelationshipRef string
	Disposition     string
	Reason          string
	Splits          []SubmissionRelationshipSplitInput
}

type SubmissionPredicateRegistrationInput struct {
	RelationshipRef    string
	PredicateKey       string
	SubjectKind        string
	ObjectKind         string
	RelationshipKind   string
	CurrentCardinality string
}

type CommitSubmissionAssessmentInput struct {
	RememberCommitScope
	AssessmentID             string
	Items                    []SubmissionAssessmentItemInput
	EntityResolutions        []SubmissionAssessmentEntityResolutionInput
	RelationshipObservations []SubmissionAssessmentRelationshipObservationInput
	PredicateRegistrations   []SubmissionPredicateRegistrationInput
	RelationshipResults      []SubmissionRelationshipResultInput
	Payload                  map[string]any
}

// SynchronousRememberCommitInput contains all request-owned state needed to make
// one final Remember transaction. Provider work happens before this input is
// committed; no field represents a pre-provider reservation.
type SynchronousRememberCommitInput struct {
	TeamID          string
	OwnerProfileID  string
	IngestID        string
	SpaceID         string
	SpaceGeneration int64
	IdempotencyKey  string
	RequestHash     string
	SourceSummary   string
	Proposal        map[string]any
	Metadata        map[string]any
	Evidence        []EvidenceInput
	AssessmentID    string
	AssessmentJSON  json.RawMessage
	ProviderTurns   int
	InputTokens     int
	OutputTokens    int
	AssessorTurns   int
	Duration        time.Duration
	// StartedAt lets the terminal repository write the complete request duration.
	StartedAt     time.Time
	CorrelationID string
	PublicResult  map[string]any
	Commit        CommitSubmissionAssessmentInput
}

type CommitSubmissionAssessmentResult struct {
	Status              string
	OutcomeIDs          []string
	RelationshipResults []RelationshipDecisionResult
	SearchDocuments     []SearchDocumentResult
	EntityResolutionIDs []string
}

type SubmissionAssessmentSecurityQuarantineInput struct {
	FragmentID string
	SecurityEventDraft
}
