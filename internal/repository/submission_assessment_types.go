package repository

import (
	"context"
	"encoding/json"
	"time"
)

// SubmissionAssessmentRepository is the run-scoped, append-once assessment
// boundary. One row represents the complete assessor conversation for every
// evidence item in the placement run.
type SubmissionAssessmentRepository interface {
	LoadSubmissionAssessment(ctx context.Context, input LoadSubmissionAssessmentInput) (*SubmissionAssessment, error)
	ReserveSubmissionAssessorAttempt(ctx context.Context, input ReserveSubmissionAssessorAttemptInput) (bool, error)
	PersistSubmissionAssessment(ctx context.Context, input PersistSubmissionAssessmentInput) (*SubmissionAssessment, bool, error)
	AppendSubmissionAssessmentRevision(ctx context.Context, input AppendSubmissionAssessmentRevisionInput) (*SubmissionAssessment, bool, error)
	CommitSubmissionAssessment(ctx context.Context, input CommitSubmissionAssessmentInput) (*CommitSubmissionAssessmentResult, error)
	CompleteSubmissionAssessment(ctx context.Context, input CompleteSubmissionAssessmentInput) (*CompleteSubmissionAssessmentResult, error)
	RequeueSubmissionAssessment(ctx context.Context, input RequeueSubmissionAssessmentInput) (*RequeueSubmissionAssessmentResult, error)
}

// InlineSubmissionAssessmentCommitter is the synchronous Remember extension
// of SubmissionAssessmentRepository. It keeps the provider callback generic
// so the repository does not depend on a concrete embedding client.
type InlineSubmissionAssessmentCommitter interface {
	CommitSubmissionAssessmentWithInlineEmbeddings(
		context.Context,
		CommitSubmissionAssessmentInput,
		InlineEmbeddingBatch,
	) (*CommitSubmissionAssessmentResult, error)
}

type SubmissionAssessmentRunScope struct {
	TeamID           string
	OwnerProfileID   string
	IngestID         string
	PlacementRunID   string
	CorrelationID    string
	WorkerID         string
	ExpectedAttempts int
	MaxAttempts      int
}

type LoadSubmissionAssessmentInput struct {
	TeamID         string
	OwnerProfileID string
	PlacementRunID string
}

type ReserveSubmissionAssessorAttemptInput struct {
	SubmissionAssessmentRunScope
}

type PersistSubmissionAssessmentInput struct {
	TeamID                    string
	OwnerProfileID            string
	IngestID                  string
	PlacementRunID            string
	RequestID                 string
	AssessorContractVersion   string
	Model                     string
	Tokenizer                 string
	ProviderTurns             int
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
}

type SubmissionAssessment struct {
	TeamID                    string
	AssessmentID              string
	OwnerProfileID            string
	IngestID                  string
	PlacementRunID            string
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

type AppendSubmissionAssessmentRevisionInput struct {
	SubmissionAssessmentRunScope
	AssessmentID              string
	ProviderTurns             int
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
}

type SubmissionAssessmentItemInput struct {
	PlacementItemID string
	FragmentID      string
}

type SubmissionAssessmentEntityResolutionInput struct {
	PlacementItemID string
	Resolution      PlacementEntityResolutionInput
}

type SubmissionAssessmentRelationshipObservationInput struct {
	PlacementItemID string
	RelationshipRef string
	SplitIndex      int
	Observation     PlacementRelationshipDecisionInput
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
	SubmissionAssessmentRunScope
	AssessmentID             string
	Items                    []SubmissionAssessmentItemInput
	EntityResolutions        []SubmissionAssessmentEntityResolutionInput
	RelationshipObservations []SubmissionAssessmentRelationshipObservationInput
	PredicateRegistrations   []SubmissionPredicateRegistrationInput
	RelationshipResults      []SubmissionRelationshipResultInput
	Payload                  map[string]any
}

type CommitSubmissionAssessmentResult struct {
	Status              string
	OutcomeIDs          []string
	FirstDisposition    *PlacementFirstDisposition
	RelationshipResults []RelationshipDecisionResult
	SearchDocuments     []SearchDocumentResult
	EntityResolutionIDs []string
}

type SubmissionAssessmentSecurityQuarantineInput struct {
	FragmentID string
	SecurityEventDraft
}

type CompleteSubmissionAssessmentInput struct {
	SubmissionAssessmentRunScope
	OutcomeKind                     string
	Status                          string
	Category                        string
	Payload                         map[string]any
	SecurityQuarantines             []SubmissionAssessmentSecurityQuarantineInput
	RelationshipResults             []SubmissionRelationshipResultInput
	DefaultRelationshipResultReason string
}

type CompleteSubmissionAssessmentResult struct {
	Status           string
	OutcomeIDs       []string
	FirstDisposition *PlacementFirstDisposition
}

type RequeueSubmissionAssessmentInput struct {
	SubmissionAssessmentRunScope
	OutcomeKind            string
	Payload                map[string]any
	RetryAfter             time.Duration
	ReleaseAssessorAttempt bool
	AssessorTurnsReserved  int
}

type RequeueSubmissionAssessmentResult struct {
	Status        string
	OutcomeIDs    []string
	NextAttemptAt *time.Time
}
