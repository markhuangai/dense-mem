package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrSubmissionNotFound           = errors.New("submission not found")
	ErrSubmissionAssessmentNotFound = errors.New("submission assessment not found")
	ErrSubmissionLeaseConflict      = errors.New("submission lease conflict")
	ErrSubmissionConflict           = errors.New("submission conflict")
)

// SubmissionRepository isolates raw evidence until an assessor response has
// passed deterministic validation. Canonical evidence and semantic records are
// deliberately outside this staging boundary.
type SubmissionRepository interface {
	CreateSubmission(ctx context.Context, input CreateSubmissionInput) (*Submission, error)
	GetSubmissionStatus(ctx context.Context, input GetSubmissionStatusInput) (*SubmissionStatus, error)
	ClaimNextSubmission(ctx context.Context, teamID, workerID string, lease time.Duration) (*SubmissionClaim, error)
	LoadClaimedSubmission(ctx context.Context, input LoadClaimedSubmissionInput) (*Submission, error)
	LoadSubmissionAssessment(ctx context.Context, input LoadSubmissionAssessmentInput) (*SubmissionAssessment, error)
	PersistSubmissionAssessment(ctx context.Context, input PersistSubmissionAssessmentInput) (*SubmissionAssessment, bool, error)
	PromoteSubmission(ctx context.Context, input PromoteSubmissionInput) (*SubmissionPromotionResult, error)
	RequeueSubmission(ctx context.Context, input RequeueSubmissionInput) error
	CompleteSubmission(ctx context.Context, input CompleteSubmissionInput) error
	QuarantineSubmission(ctx context.Context, input QuarantineSubmissionInput) error
	CleanupExpiredSubmissions(ctx context.Context, now time.Time, limit int) (int64, error)
}

type CreateSubmissionInput struct {
	TeamID                          string
	OwnerProfileID                  string
	ActorCredentialID               string
	ActorAuthMethod                 string
	ActorRole                       string
	ActorScopes                     []string
	CorrelationID                   string
	IdempotencyKey                  string
	RequestHash                     string
	SourceSummary                   string
	Proposal                        map[string]any
	Evidence                        []SubmissionEvidenceInput
	ReplacesQuarantinedSubmissionID string
}

type SubmissionEvidenceInput struct {
	Content                string
	SourceType             string
	Source                 string
	SourceGroup            string
	Authority              string
	SourceKey              string
	SourceRevision         string
	PreviousSourceRevision string
	SupersedesEvidenceIDs  []string
	IdempotencyKey         string
	Labels                 []string
	Metadata               map[string]any
}

type SubmissionEvidence struct {
	EvidenceIndex int
	Content       string
	ContentHash   string
	SubmissionEvidenceInput
}

type Submission struct {
	TeamID                          string
	SubmissionID                    string
	OwnerProfileID                  string
	ActorCredentialID               string
	ActorAuthMethod                 string
	ActorRole                       string
	ActorScopes                     []string
	CorrelationID                   string
	RequestHash                     string
	SourceSummary                   string
	CreatedAt                       time.Time
	Status                          string
	Attempts                        int
	MaxAttempts                     int
	LeaseUntil                      *time.Time
	WorkerID                        string
	ErrorCode                       string
	CanonicalIngestID               string
	ReplacesQuarantinedSubmissionID string
	QuarantineExpiresAt             *time.Time
	Proposal                        map[string]any
	Evidence                        []SubmissionEvidence
	Outcomes                        []SubmissionOutcome
}

type SubmissionClaim struct {
	TeamID         string
	SubmissionID   string
	OwnerProfileID string
	Attempts       int
	MaxAttempts    int
	CreatedAt      time.Time
	LeaseUntil     *time.Time
}

type GetSubmissionStatusInput struct {
	TeamID         string
	OwnerProfileID string
	SubmissionID   string
}

type SubmissionStatus struct {
	SubmissionID         string
	ProcessingState      string
	SearchState          string
	Evidence             []SubmissionEvidenceStatus
	RelationshipOutcomes []SubmissionRelationshipOutcome
	Errors               []SubmissionStatusError
	QuarantineExpiresAt  *time.Time
}

type SubmissionEvidenceStatus struct {
	EvidenceIndex int    `json:"evidence_index"`
	Status        string `json:"status"`
	SearchState   string `json:"search_state"`
	ReasonCode    string `json:"reason_code,omitempty"`
}

type SubmissionRelationshipOutcome struct {
	ProposalID     string `json:"proposal_id"`
	RelationshipID string `json:"relationship_id,omitempty"`
	Status         string `json:"status"`
	ReasonCode     string `json:"reason_code,omitempty"`
}

type SubmissionStatusError struct {
	Code string `json:"code"`
}

type SubmissionOutcome struct {
	OutcomeKind   string
	EvidenceIndex *int
	ProposalID    string
	Status        string
	ReasonCode    string
	Details       map[string]any
	CreatedAt     time.Time
}

type LoadClaimedSubmissionInput struct {
	TeamID         string
	OwnerProfileID string
	SubmissionID   string
	WorkerID       string
	Attempts       int
}

type LoadSubmissionAssessmentInput struct {
	TeamID         string
	OwnerProfileID string
	SubmissionID   string
}

type PersistSubmissionAssessmentInput struct {
	TeamID                    string
	OwnerProfileID            string
	SubmissionID              string
	WorkerID                  string
	ExpectedAttempts          int
	RequestID                 string
	Model                     string
	Tokenizer                 string
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
}

type SubmissionAssessment struct {
	AssessmentID              string
	SubmissionID              string
	RequestID                 string
	Model                     string
	Tokenizer                 string
	InputTokens               int
	OutputTokens              int
	CandidateContextTokens    int
	CandidateContextTruncated bool
	NormalizedResponse        json.RawMessage
	ResponseHash              string
	ValidatedAt               time.Time
}

// PromoteSubmission is the only path from staged raw evidence to canonical
// knowledge. The repository creates the supplied canonical IDs and semantic
// commits in the same transaction that completes and clears the staging data.
type PromoteSubmissionInput struct {
	TeamID                          string
	OwnerProfileID                  string
	SubmissionID                    string
	WorkerID                        string
	ExpectedAttempts                int
	Lease                           time.Duration
	Canonical                       CreateIngestInput
	Commits                         []CommitPlacementSemanticInput
	EvidenceOutcomes                []SubmissionEvidenceStatus
	ReplacesQuarantinedSubmissionID string
}

type SubmissionPromotionResult struct {
	CanonicalIngestID    string
	RelationshipOutcomes []SubmissionRelationshipOutcome
	EvidenceSearchState  string
	PlacementRunID       string
}

type RequeueSubmissionInput struct {
	TeamID           string
	OwnerProfileID   string
	SubmissionID     string
	WorkerID         string
	ExpectedAttempts int
	ReasonCode       string
	RetryAfter       time.Duration
}

type CompleteSubmissionInput struct {
	TeamID               string
	OwnerProfileID       string
	SubmissionID         string
	WorkerID             string
	ExpectedAttempts     int
	Status               string
	ReasonCode           string
	CanonicalIngestID    string
	EvidenceOutcomes     []SubmissionEvidenceStatus
	RelationshipOutcomes []SubmissionRelationshipOutcome
}

type QuarantineSubmissionInput struct {
	TeamID           string
	OwnerProfileID   string
	SubmissionID     string
	WorkerID         string
	ExpectedAttempts int
	ReasonCode       string
	EvidenceOutcomes []SubmissionEvidenceStatus
}
