// Package contract contains the Dream capability's dependency-safe ports and
// value objects. Storage adapters implement these contracts; application
// policy never depends on the legacy repository package.
package contract

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type DreamCycleRun struct {
	TeamID                   string
	RunID                    string
	InitiatedByProfileID     string
	RunDate                  string
	WindowKey                string
	Status                   string
	ScheduledFor             *time.Time
	LeaseToken               string
	LeaseUntil               *time.Time
	AttemptCount             int
	InputCount               int
	CreatedHypotheses        int
	RejectedHypotheses       int
	ProviderModel            string
	ProviderTurns            int
	ProviderInputTokens      int
	ProviderOutputTokens     int
	AttemptedPaths           int
	ProviderProposals        int
	OutcomeSummary           map[string]int
	Error                    string
	StartedAt                time.Time
	CompletedAt              *time.Time
	Claimed                  bool
	Lane                     domain.DreamLane
	EvidenceTargets          int
	EvaluatedEvidenceTargets int
}

// DreamDiagnosticCapture is a bounded operator-facing record of one Dream
// execution phase. Its details are a protected projection; they are never
// submitted as evidence or treated as semantic state.
type DreamDiagnosticCapture struct {
	TeamID        string
	CaptureID     string
	RunID         string
	HypothesisID  string
	Phase         string
	Outcome       string
	Cause         string
	Details       map[string]any
	Payload       []byte
	CaptureState  string
	CaptureReason string
	CapturedAt    *time.Time
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type DreamDiagnosticListInput struct {
	TeamID       string
	RunID        string
	HypothesisID string
	Limit        int
	Cursor       string
}

type DreamDiagnosticPage struct {
	Items      []DreamDiagnosticCapture
	NextCursor string
}

type EvidenceDerivationSource struct {
	EvidenceID       string
	FragmentID       string
	SourceID         string
	SourceRevisionID string
	SourceGroupKey   string
	SpanStart        int
	SpanEnd          int
	Quote            string
	Authority        string
}

type DreamDerivationSource struct {
	PremisePosition     int
	RelationshipID      string
	RelationshipVersion int
	SupportID           string
	ObservationID       string
	FragmentID          string
	SourceID            string
	SourceRevisionID    string
	SourceGroupKey      string
	SpanStart           int
	SpanEnd             int
	Quote               string
	Authority           string
}

type HypothesisRecord struct {
	TeamID                        string
	HypothesisID                  string
	CreatedByProfileID            string
	Status                        string
	Statement                     string
	Rationale                     string
	Likelihood                    *float64
	Confidence                    *float64
	SubjectEntityID               string
	PredicateKey                  string
	PredicateVersion              int
	ObjectEntityID                string
	ObjectValueID                 string
	SourceRefs                    []map[string]any
	SourceVersions                map[string]int
	SourceOwnerProfileIDs         []string
	ContentHash                   string
	TargetIdentity                string
	Derivations                   []DreamDerivationSource
	CycleRunID                    string
	GeneratorKind                 string
	GeneratorVersion              string
	InvalidatedReason             string
	SubmittedIngestID             string
	SubmittedIngestIdempotencyKey string
	SubmittedIngestRequestHash    string
	SubmittedDecision             string
	SubmittedAt                   *time.Time
	Payload                       map[string]any
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
	Lane                          domain.DreamLane
	SourceEvidenceIDs             []string
	EvidenceDerivations           []EvidenceDerivationSource
}

type ListHypothesesInput struct {
	TeamID    string
	Status    string
	Limit     int
	Cursor    string
	Sort      string
	Direction string
}

type GetHypothesisInput struct {
	TeamID       string
	HypothesisID string
}
