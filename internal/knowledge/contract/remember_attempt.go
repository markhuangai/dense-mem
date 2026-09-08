package contract

import (
	"context"
	"time"
)

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

// RememberFailureArtifactInput is intentionally failure-only. Its content must
// already be scrubbed of credentials, prompts, provider responses, and database
// errors by the application service.
type RememberFailureArtifactInput struct {
	ArtifactID, ArtifactKind, ContentType string
	Content                               []byte
	CapturedAt, ExpiresAt                 time.Time
}

type RememberFailureRecordInput struct {
	Attempt   RememberAttemptRecordInput
	Artifacts []RememberFailureArtifactInput
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
// Events, and Artifacts are populated only by the single-attempt detail read;
// list records contain scalar metadata only.
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
	Artifacts                                   []RememberFailureArtifactDescriptor
}

type RememberAttemptDiagnosticEvent struct {
	SequenceNo int
	Phase      string
	EventKind  string
	Outcome    string
	Metadata   map[string]any
	CreatedAt  time.Time
}

type RememberFailureArtifactDescriptor struct {
	ArtifactID          string
	ArtifactKind        string
	ContentType         string
	ByteCount           int64
	ContentSHA256       string
	CapturedAt          time.Time
	ExpiresAt           time.Time
	RetainedByLegalHold bool
}

type RememberFailureArtifact struct {
	TeamID              string
	ArtifactID          string
	AttemptID           string
	ArtifactKind        string
	ContentType         string
	Content             []byte
	ByteCount           int64
	ContentSHA256       string
	CapturedAt          time.Time
	ExpiresAt           time.Time
	RetainedByLegalHold bool
}

type RememberAttemptDiagnosticRecordPage struct {
	Records []RememberAttemptDiagnosticRecord
	Total   int64
}
