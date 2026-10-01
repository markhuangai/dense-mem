// Package contract defines the narrow ports owned by the operations capability.
package contract

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// OperationLogRepository persists structured operation logs.
type OperationLogRepository interface {
	AppendBatch(context.Context, []domain.OperationLog) error
	List(context.Context, domain.OperationLogFilter) (*domain.OperationLogPage, error)
	PruneBefore(context.Context, time.Time) error
}

func NormalizeOperationLogFilter(filter domain.OperationLogFilter) domain.OperationLogFilter {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	filter.Sort = strings.ToLower(strings.TrimSpace(filter.Sort))
	if filter.Sort != "severity" {
		filter.Sort = "timestamp"
	}
	filter.Direction = strings.ToLower(strings.TrimSpace(filter.Direction))
	if filter.Direction != "asc" {
		filter.Direction = "desc"
	}
	filter.Severity = strings.ToUpper(strings.TrimSpace(filter.Severity))
	filter.Event = strings.TrimSpace(filter.Event)
	filter.CorrelationID = strings.TrimSpace(filter.CorrelationID)
	filter.InvocationID = strings.TrimSpace(filter.InvocationID)
	filter.RequestHash = strings.TrimSpace(filter.RequestHash)
	filter.AttemptID = strings.TrimSpace(filter.AttemptID)
	filter.Classification = strings.TrimSpace(filter.Classification)
	filter.ReferenceType = strings.TrimSpace(filter.ReferenceType)
	filter.ReferenceID = strings.TrimSpace(filter.ReferenceID)
	if filter.From != nil {
		value := filter.From.UTC()
		filter.From = &value
	}
	if filter.To != nil {
		value := filter.To.UTC()
		filter.To = &value
	}
	return filter
}

func NormalizeOperationLogSeverity(severity string) string {
	severity = strings.ToUpper(strings.TrimSpace(severity))
	if severity == "" {
		return "INFO"
	}
	return severity
}

// OperationLogSinkProber verifies the required operation-log write path
// without committing a readiness probe row.
type OperationLogSinkProber interface {
	ProbeOperationLogSink(context.Context) error
}

// UsageMetricsRepository persists bounded runtime usage aggregates.
type UsageMetricsRepository interface {
	UpsertBuckets(context.Context, uuid.UUID, []domain.UsageMetricBucket) error
	PruneBefore(context.Context, time.Time) error
	Snapshot(context.Context, domain.UsageMetricsFilter) (*domain.UsageMetricsSnapshot, error)
}

// TelemetryLifecycleReader reads authoritative lifecycle aggregates.
type TelemetryLifecycleReader interface {
	ReadTelemetryLifecycle(context.Context, TelemetryLifecycleFilter, time.Time, time.Time) (TelemetryLifecycleSnapshot, error)
}

type TelemetryLifecycleFilter struct {
	TeamID    *uuid.UUID
	ProfileID *uuid.UUID
}

type TelemetryLifecycleSnapshot struct {
	Transitions map[string]float64
	Corrections float64
	Current     map[string]float64
}

// OperationalTelemetryReader reads bounded system-wide aggregates from the
// canonical ledgers. It never returns tenant or profile identifiers.
type OperationalTelemetryReader interface {
	ReadOperationalTelemetry(context.Context) (OperationalTelemetrySnapshot, error)
}

type OperationalTelemetrySnapshot struct {
	DreamRuns               []DreamRunTelemetry
	Hypotheses              []HypothesisTelemetry
	Feedback                []WindowedTelemetryCount
	RememberAttempts        []WindowedTelemetryCount
	ConfirmedRelationships  []WindowedTelemetryCount
	RelationshipTransitions []WindowedTelemetryCount
	RelationshipCorrections []WindowedTelemetryCount
	RelationshipsCurrent    []NamedTelemetryCount
}

type DreamRunTelemetry struct {
	Window             string
	Lane               string
	Status             string
	Runs               float64
	InputTargets       float64
	EvidenceTargets    float64
	EvaluatedTargets   float64
	ProviderProposals  float64
	CreatedHypotheses  float64
	RejectedHypotheses float64
}

type HypothesisTelemetry struct {
	Lane             string
	Status           string
	Count            float64
	BacklogCount     float64
	OldestBacklogAge float64
}

type WindowedTelemetryCount struct {
	Window string
	Kind   string
	Count  float64
}

type NamedTelemetryCount struct {
	Kind  string
	Count float64
}

// TelemetryPricingReader provides the current operator-managed rate card.
// CachedTelemetryPricingRuntimeConfig must not perform a storage read because
// provider paths use it while recording usage.
type TelemetryPricingReader interface {
	TelemetryPricingRuntimeConfig(context.Context) (domain.TelemetryPricingRuntimeConfig, error)
	CachedTelemetryPricingRuntimeConfig() (domain.TelemetryPricingRuntimeConfig, bool)
}

// AuthorityReader reads the compatibility marker used by active boot.
type AuthorityReader interface {
	GetLatestMarker(context.Context) (*domain.CompatibilityMarker, error)
}

// AuthorityStore reads and, during bootstrap only, records the compatibility marker.
type AuthorityStore interface {
	AuthorityReader
	CommitFreshAuthority(context.Context, CommitFreshAuthorityInput) (*domain.CompatibilityMarker, error)
}

type CommitFreshAuthorityInput struct {
	MarkerVersion string
	Metadata      map[string]any
	Now           time.Time
}
