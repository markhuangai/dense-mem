package contract

import (
	"context"
	"strings"
	"time"
)

// DiagnosticCaptureState derives the bounded state used by both the capture
// recorder and durable repository when a caller did not provide one.
func DiagnosticCaptureState(captureState, outcome string, requestBodyBytes, responseBodyBytes int) string {
	if state := strings.TrimSpace(captureState); state != "" {
		return state
	}
	switch strings.TrimSpace(outcome) {
	case "provider_not_called":
		return "provider_not_called"
	case "no_response":
		return "no_response"
	case "response_read_failed":
		return "interrupted"
	case "response_too_large":
		return "truncated"
	case "not_captured":
		return "not_captured"
	default:
		if requestBodyBytes == 0 && responseBodyBytes == 0 {
			return "not_captured"
		}
		return "captured"
	}
}

type RememberAttemptRecordInput struct {
	TeamID, OwnerProfileID, AttemptID string
	SpaceID                           string
	SpaceGeneration                   int64
	IdempotencyKey, RequestHash       string
	ContractVersion, SubmissionKind   string
	Outcome, FailedPhase, ErrorCode   string
	Retryable                         bool
	// RetryabilitySet distinguishes an explicit false from legacy callers that
	// predate the retryability field. New application writers set this flag.
	RetryabilitySet                  bool
	CorrelationID                    string
	PublicResult                     map[string]any
	EvidenceCount, RelationshipCount int
	DocumentCount, AssessorTurns     int
	Duration                         time.Duration
}

type RememberAttempt struct {
	AttemptID, RequestHash, ContractVersion, Outcome string
	Retryable                                        bool
	PublicResult                                     map[string]any
}

type RememberAttemptLookupInput struct {
	TeamID, OwnerProfileID, IdempotencyKey string
}

type RememberAttemptLookup interface {
	LoadRememberAttempt(context.Context, RememberAttemptLookupInput) (*RememberAttempt, error)
}

type RememberFailureRecordInput struct {
	Attempt     RememberAttemptRecordInput
	Diagnostics []RememberAttemptDiagnosticInput
}

// RememberAttemptDiagnosticInput is an operator-only, failure-scoped exchange
// captured for one terminal Remember attempt. It contains bodies only; request
// headers and transport credentials are never part of the record.
type RememberAttemptDiagnosticInput struct {
	DiagnosticID        string
	SequenceNo          int
	Kind                string
	Component           string
	Model               string
	RequestBody         []byte
	ResponseBody        []byte
	RequestContentType  string
	ResponseContentType string
	StatusCode          int
	Outcome             string
	CaptureState        string
	CapturedAt          time.Time
	ExpiresAt           time.Time
}

// RememberAttemptDiagnosticFilter is the normalized filter supplied by the
// diagnostics service for the control-portal attempt read model. It
// deliberately uses the durable attempt outcome rather than a placement
// processing state.
type RememberAttemptDiagnosticFilter struct {
	TeamID  string
	Outcome string
	Limit   int
	Offset  int
}

// RememberAttemptDiagnosticRecord is a control-only projection. PublicResult,
// Events, and Diagnostics are populated only by the single-attempt detail
// read; list records contain scalar metadata only.
type RememberAttemptDiagnosticRecord struct {
	TeamID, TeamName, OwnerProfileID, AttemptID string
	SpaceID, CanonicalAttemptID                 string
	SpaceGeneration                             int64
	ContractVersion, SubmissionKind             string
	Outcome, FailedPhase, ErrorCode             string
	Retryable                                   bool
	CorrelationID                               string
	EvidenceCount, RelationshipCount            int
	DocumentCount, AssessorTurns                int
	Duration                                    time.Duration
	CreatedAt                                   time.Time
	CompletedAt                                 *time.Time
	PublicResult                                map[string]any
	Events                                      []RememberAttemptDiagnosticEvent
	Diagnostics                                 []RememberAttemptDiagnosticRecordItem
}

type RememberAttemptDiagnosticRecordItem struct {
	DiagnosticID        string
	SequenceNo          int
	Kind                string
	Component           string
	Model               string
	RequestBody         []byte
	ResponseBody        []byte
	RequestContentType  string
	ResponseContentType string
	StatusCode          int
	Outcome             string
	CaptureState        string
	CapturedAt          time.Time
	ExpiresAt           time.Time
	RetainedByLegalHold bool
}

type RememberAttemptDiagnosticEvent struct {
	SequenceNo int
	Phase      string
	EventKind  string
	Outcome    string
	Metadata   map[string]any
	CreatedAt  time.Time
}

type RememberAttemptDiagnosticRecordPage struct {
	Records []RememberAttemptDiagnosticRecord
	Total   int64
}
