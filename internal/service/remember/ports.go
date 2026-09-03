package remember

import (
	"context"
	"errors"
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

type EvidenceInput struct {
	Content                       string
	ForceInsert                   bool
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
