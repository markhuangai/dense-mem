package remember

import (
	"context"
	"errors"
	"fmt"
)

// The intake boundary owns these values. Adapters translate their storage
// records into this small contract instead of making the application depend on
// a repository package.
var (
	ErrIdempotencyConflict    = errors.New("remember: idempotency conflict")
	ErrSourceRevisionConflict = errors.New("remember: source revision conflict")
	ErrTeamInactive           = errors.New("remember: team is inactive")
)

type RememberValidationIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RememberValidationError struct {
	Issues          []RememberValidationIssue `json:"issues"`
	IssuesTruncated bool                      `json:"issues_truncated"`
}

func (e *RememberValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "remember validation failed"
	}
	return e.Issues[0].Message
}

// SynchronousProcessor is the v2.6.1 request-owned boundary. Implementations
// receive the fully validated request and return only a terminal structured
// result; they own any private persistence orchestration needed to execute it.
type SynchronousProcessor interface {
	ProcessRemember(context.Context, RememberProcessRequest) (*SubmissionStatusResult, error)
}

// RememberProcessError carries the durable terminal status alongside the
// bounded operational cause so transports can correlate an error result with
// the persisted attempt instead of inventing a new submission ID.
type RememberProcessError struct {
	Status *SubmissionStatusResult
	Err    error
}

func (e *RememberProcessError) Error() string {
	if e == nil {
		return "remember: processor failed"
	}
	if e.Err == nil {
		return "remember: processor failed"
	}
	return fmt.Sprintf("remember: processor failed: %v", e.Err)
}

func (e *RememberProcessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type RememberProcessRequest struct {
	TeamID                   string
	OwnerProfileID           string
	SpaceID                  string
	SpaceGeneration          int64
	IdempotencyKey           string
	RequestHash              string
	SourceSummary            string
	Proposal                 map[string]any
	Metadata                 map[string]any
	Evidence                 []EvidenceInput
	SecuritySignals          []SubmissionSecurityBatchSignal
	SecuritySignalsTruncated bool
	SecurityRejected         bool
}

type EvidenceInput struct {
	Content                       string
	ContentHash                   string
	SourceType                    string
	Authority                     string
	SourceRef                     string
	SourceKey                     string
	SourceRevisionToken           string
	ExpectedPreviousRevisionToken string
	SourceRevisionContentHash     string
	SourceRevisionEnvelope        map[string]any
	SupersedesEvidenceIDs         []string
	Labels                        []string
	Metadata                      map[string]any
	InitialEvent                  *SecurityEventDraft
}

type SecurityEventDraft struct {
	EventKind string
	Decision  string
	Reason    string
	Signals   []SecuritySignalInput
	Metadata  map[string]any
}

type SecuritySignalInput struct {
	Kind      string
	Severity  string
	SpanStart int
	SpanEnd   int
	Metadata  map[string]any
}

type EvidenceFragment struct {
	FragmentID            string
	EvidenceIndex         int
	Content               string
	ContentHash           string
	Authority             string
	SourceID              string
	SourceRevisionID      string
	SupersededEvidenceIDs []string
}

type SubmissionRelationshipSplit struct {
	SplitIndex          int    `json:"split_index"`
	RelationshipID      string `json:"relationship_id"`
	RelationshipVersion int    `json:"relationship_version"`
	Status              string `json:"status"`
}

type SubmissionRelationshipResult struct {
	RelationshipRef string                        `json:"ref"`
	Disposition     string                        `json:"disposition"`
	Reason          string                        `json:"reason,omitempty"`
	Splits          []SubmissionRelationshipSplit `json:"splits"`
}

// SecurityRejectionAuditor receives only bounded scanner metadata. Evidence,
// decoded content, and provider payloads are intentionally absent.
type SecurityRejectionAuditor interface {
	RecordSecurityRejection(context.Context, SecurityRejectionAuditInput) error
}

type SecurityRejectionAuditInput struct {
	EventID          string
	TeamID           string
	ActorProfileID   string
	ActorRole        string
	CorrelationID    string
	Surface          string
	ReasonCode       string
	EvidenceCount    int
	Signals          []SecurityRejectionAuditSignal
	SignalsTruncated bool
}

type SecurityRejectionAuditSignal struct {
	EvidenceIndex int
	Source        string
	Kind          string
	RuleID        string
	Severity      string
	SpanStart     int
	SpanEnd       int
}
