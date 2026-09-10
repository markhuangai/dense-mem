// Package contract defines the narrow ports owned by the operations capability.
package contract

import (
	"context"
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

// UsageMetricsRepository persists bounded runtime usage aggregates.
type UsageMetricsRepository interface {
	UpsertBuckets(context.Context, []domain.UsageMetricBucket) error
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
