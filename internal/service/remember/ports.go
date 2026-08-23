package remember

import (
	"context"
	"errors"
	"time"
)

// The intake boundary owns these values. Adapters translate their storage
// records into this small contract instead of making the application depend on
// a repository package.
var (
	ErrIdempotencyConflict    = errors.New("remember: idempotency conflict")
	ErrSourceRevisionConflict = errors.New("remember: source revision conflict")
	ErrTeamInactive           = errors.New("remember: team is inactive")
)

type IntakePort interface {
	Stage(context.Context, StageRequest) (*StageResult, error)
	Status(context.Context, StatusRequest) (*StageResult, error)
}

type StageRequest struct {
	TeamID            string
	OwnerProfileID    string
	SpaceID           string
	SpaceGeneration   int64
	IdempotencyKey    string
	RequestHash       string
	SourceSummary     string
	Status            string
	TelemetryRemember bool
	Proposal          map[string]any
	Metadata          map[string]any
	Evidence          []EvidenceInput
}

type StatusRequest struct {
	TeamID         string
	OwnerProfileID string
	SubmissionID   string
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
	IdempotencyKey                string
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

type PlacementItem struct {
	PlacementItemID string
	FragmentID      string
	ClaimKey        string
	EvidenceIndex   int
	Status          string
	Category        string
	Version         int
	Result          map[string]any
}

type FirstDisposition struct {
	Status      string
	CreatedAt   time.Time
	CompletedAt time.Time
	IsRemember  bool
}

type StageResult struct {
	TeamID              string
	OwnerProfileID      string
	SubmissionID        string
	PlacementRunID      string
	Status              string
	CorrelationID       string
	Attempts            int
	MaxAttempts         int
	Existing            bool
	Proposal            map[string]any
	Evidence            []EvidenceFragment
	Items               []PlacementItem
	FirstDisposition    *FirstDisposition
	SubmittedAt         *time.Time
	NextAttemptAt       *time.Time
	StartedAt           *time.Time
	UpdatedAt           *time.Time
	CompletedAt         *time.Time
	QuarantineExpiresAt *time.Time
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
