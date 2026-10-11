package contract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	remember "github.com/markhuangai/dense-mem/internal/remember/contract"
)

const (
	MaxEvents         = 20
	MaxWindows        = 8
	WindowTokens      = 8192
	SegmentRunes      = 512
	ContextEvents     = 10
	ContextTokens     = 2048
	MaxExcerpts       = 100
	ExtractionVersion = "session_extraction_v1"
)

var (
	ErrInvalidInput            = errors.New("session: invalid input")
	ErrUnauthorized            = errors.New("session: private write authorization required")
	ErrEventConflict           = errors.New("session: event conflict")
	ErrRequestConflict         = errors.New("session: idempotency conflict")
	ErrOriginalRequestRequired = errors.New("session: original request required")
	ErrBudget                  = errors.New("session: input budget exceeded")
	ErrNotFound                = errors.New("session: submission not found")
	ErrStale                   = errors.New("session: private space or evidence changed")
	ErrSecurity                = errors.New("session: user evidence rejected by security policy")
)

type Event struct {
	EventID    string  `json:"event_id"`
	Text       string  `json:"text"`
	OccurredAt *string `json:"occurred_at,omitempty"`
}

type Request struct {
	IdempotencyKey string  `json:"idempotency_key"`
	Framework      string  `json:"framework"`
	AppName        string  `json:"app_name"`
	UserID         string  `json:"user_id"`
	SessionID      string  `json:"session_id"`
	Events         []Event `json:"events"`
}

type Scope struct {
	TeamID          string
	OwnerProfileID  string
	SpaceID         string
	SpaceGeneration int64
}

type Segment struct {
	Ref        string `json:"ref"`
	EventIndex int    `json:"event_index"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Text       string `json:"text"`
}

type Window struct {
	Index  int       `json:"index"`
	Core   []Segment `json:"core"`
	Before *Segment  `json:"before,omitempty"`
	After  *Segment  `json:"after,omitempty"`
}

type Intake struct {
	Scope             Scope
	Request           Request
	RequestHash       string
	Windows           []Window
	DuplicateResults  map[string]EventResult
	ExtractionVersion string
	Tokenizer         string
}

type PriorEvent struct {
	Event       Event    `json:"event"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Submission struct {
	ID                  string
	Intake              Intake
	AcceptedEventCount  int
	DuplicateEventCount int
	NewEventIndices     []int
	Prior               []PriorEvent
	Extractions         map[int]json.RawMessage
	Linked              json.RawMessage
	Result              *Result
}

type EventResult struct {
	EventID         string   `json:"event_id"`
	Disposition     string   `json:"disposition"`
	ProcessingState string   `json:"processing_state"`
	EvidenceIDs     []string `json:"evidence_ids"`
}

type Error struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	NextAction  string `json:"next_action"`
	Remediation string `json:"remediation"`
}

type Result struct {
	ContractVersion     string                                   `json:"contract_version"`
	SubmissionID        string                                   `json:"submission_id"`
	SubmissionKind      string                                   `json:"submission_kind"`
	CorrelationID       string                                   `json:"correlation_id"`
	AcceptedEventCount  int                                      `json:"accepted_event_count"`
	DuplicateEventCount int                                      `json:"duplicate_event_count"`
	ProcessingState     string                                   `json:"processing_state"`
	SearchState         string                                   `json:"search_state"`
	Events              []EventResult                            `json:"events"`
	RelationshipResults []knowledge.SubmissionRelationshipResult `json:"relationship_results"`
	Errors              []Error                                  `json:"errors"`
	Warnings            []string                                 `json:"warnings,omitempty"`
}

type ProcessingError struct {
	Code  string
	Cause error
}

func (e *ProcessingError) Error() string { return "session preparation failed: " + e.Code }
func (e *ProcessingError) Unwrap() error { return e.Cause }

type Prepared struct {
	Commit     knowledge.SynchronousRememberCommitInput
	Embeddings []knowledge.InlineEmbeddingResult
}

type PrepareInput struct {
	Scope        Scope
	SubmissionID string
	RequestHash  string
	Evidence     []remember.EvidenceInput
	Proposal     map[string]any
	Metadata     map[string]any
}

type Preparer interface {
	PrepareSession(context.Context, PrepareInput) (*Prepared, error)
}

type Repository interface {
	WithSessionLock(context.Context, Scope, Request, func() error) error
	LookupSession(context.Context, Scope, string) (*Submission, error)
	StageSession(context.Context, Intake) (*Submission, error)
	SaveSessionExtraction(context.Context, Scope, string, int, json.RawMessage) error
	SaveSessionLinking(context.Context, Scope, string, json.RawMessage) error
	CommitSession(context.Context, Scope, string, *Prepared, Result) (*Result, error)
	RecordSessionFailure(context.Context, Scope, string, Result) error
	RecordSessionDiagnostic(context.Context, Scope, string, json.RawMessage) error
}

type API interface {
	Available(context.Context) bool
	Ingest(context.Context, Request) (*Result, error)
}

func SameEvent(left, right Event) bool {
	if left.EventID != right.EventID || left.Text != right.Text {
		return false
	}
	if left.OccurredAt == nil || right.OccurredAt == nil {
		return left.OccurredAt == nil && right.OccurredAt == nil
	}
	return *left.OccurredAt == *right.OccurredAt
}

func IdentityHash(req Request, eventID string) string {
	encoded, _ := json.Marshal([]string{req.Framework, req.AppName, req.UserID, req.SessionID, eventID})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
