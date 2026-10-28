package contract

import (
	"context"
	"errors"
	"time"

	audit "github.com/markhuangai/dense-mem/internal/audit/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
)

const MaintenancePageSize = 100
const MaintenanceDefinitionGroupSize = 20
const MaxManualBatches = 100

var (
	ErrMaintenancePaused   = errors.New("ontology maintenance paused")
	ErrMaintenanceDisabled = errors.New("ontology maintenance disabled")
	ErrWindowClosed        = errors.New("ontology maintenance window closed")
	ErrConfiguration       = errors.New("ontology maintenance configuration invalid")
	ErrLeaseLost           = errors.New("ontology maintenance lease lost")
	ErrAccounting          = errors.New("ontology maintenance accounting unavailable")
	ErrAuditUnavailable    = errors.New("ontology maintenance audit unavailable")
)

type MaintenanceWindow struct {
	ID       string                           `json:"id"`
	StartsAt time.Time                        `json:"starts_at"`
	EndsAt   time.Time                        `json:"ends_at"`
	Policy   domain.OntologyMaintenanceConfig `json:"policy"`
	Usage    MaintenanceUsage                 `json:"usage"`
}

type MaintenanceUsage struct {
	Attempts                 int64 `json:"attempts"`
	UnreconciledAttempts     int64 `json:"unreconciled_attempts"`
	UnavailableUsageAttempts int64 `json:"unavailable_usage_attempts"`
	PartialUsageAttempts     int64 `json:"partial_usage_attempts"`
	ReportedInputTokens      int64 `json:"reported_input_tokens"`
	ReportedOutputTokens     int64 `json:"reported_output_tokens"`
	ReportedTotalTokens      int64 `json:"reported_total_tokens"`
}

type MaintenanceRun struct {
	ID               string    `json:"id"`
	WindowID         string    `json:"window_id"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	Retryable        bool      `json:"retryable"`
	OperationKey     string    `json:"operation_key,omitempty"`
	MaxBatches       int       `json:"max_batches"`
	CompletedBatches int       `json:"completed_batches"`
	FailureCode      string    `json:"failure_code,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	InputTokens      int64     `json:"input_tokens,omitempty"`
	OutputTokens     int64     `json:"output_tokens,omitempty"`
}

// MaintenanceAudit prepares an entry without storage or provider I/O.
type MaintenanceAudit func(MaintenanceRun) (audit.Entry, error)

type MaintenanceClaim struct {
	ID         string
	RunID      string
	WindowID   string
	TeamID     string
	SpaceID    string
	Generation int64
	LeaseToken string
	LeaseUntil time.Time
	Sources    []SourceHandle
	Revisions  map[string]int64
}

type MaintenanceTurn struct {
	RunID      string
	RetryRunID string
	TeamID     string
	SpaceID    string
	Generation int64
	LeaseToken string
	LeaseUntil time.Time
}

type MaintenanceCounts struct {
	Eligible  int64 `json:"eligible"`
	Organized int64 `json:"organized"`
	Pending   int64 `json:"pending"`
	Ambiguous int64 `json:"ambiguous"`
	Failed    int64 `json:"failed"`
}

type MaintenanceStatus struct {
	ObservedAt             time.Time                        `json:"observed_at"`
	Paused                 bool                             `json:"paused"`
	Enabled                bool                             `json:"enabled"`
	DiscoveryComplete      bool                             `json:"discovery_complete"`
	CoverageComplete       bool                             `json:"coverage_complete"`
	Counts                 MaintenanceCounts                `json:"counts"`
	OldestPendingAt        *time.Time                       `json:"oldest_pending_at,omitempty"`
	LastSuccessfulProgress *time.Time                       `json:"last_successful_progress,omitempty"`
	Window                 *MaintenanceWindow               `json:"window,omitempty"`
	LatestRun              *MaintenanceRun                  `json:"latest_run,omitempty"`
	PendingPolicy          domain.OntologyMaintenanceConfig `json:"pending_policy"`
	ConfigurationError     string                           `json:"configuration_error,omitempty"`
	ActiveWork             int                              `json:"active_work"`
	Usage                  MaintenanceUsage                 `json:"usage"`
	HistoricalUsage        MaintenanceUsage                 `json:"historical_usage"`
}

type MaintenanceRunPage struct {
	Runs       []MaintenanceRun `json:"runs"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type MaintenanceDiscoveryCounts struct {
	InitialSources       int `json:"initial_sources"`
	EnumeratedMatches    int `json:"enumerated_matches"`
	RefreshedSources     int `json:"refreshed_sources"`
	FreshnessInvocations int `json:"freshness_invocations"`
	DistinctReceipts     int `json:"distinct_receipts"`
	Requeues             int `json:"requeues"`
	CompletedMarkers     int `json:"completed_markers"`
}

type MaintenanceDiscoveryResult struct {
	Committed bool
	Counts    MaintenanceDiscoveryCounts
	Attempted MaintenanceDiscoveryCounts
	Duration  time.Duration
}

type MaintenanceRepository interface {
	SeedDefinitions(context.Context, string, SeedInput) (SeedResult, error)
	ListRecords(context.Context, string, Kind, string, int) (Page, error)
	EnsureMaintenanceWindow(context.Context, domain.OntologyMaintenanceConfig, time.Time) (*MaintenanceWindow, error)
	CurrentMaintenanceWindow(context.Context, time.Time) (*MaintenanceWindow, error)
	ClaimMaintenanceTurn(context.Context, string, time.Time, time.Duration) (*MaintenanceTurn, error)
	DiscoverMaintenance(context.Context, MaintenanceTurn, int) (MaintenanceDiscoveryResult, error)
	ClaimMaintenanceBatch(context.Context, MaintenanceTurn, string, time.Time) (*MaintenanceClaim, error)
	ReleaseMaintenanceTurn(context.Context, MaintenanceTurn) error
	RecordMaintenanceFailure(context.Context, string, string, time.Time) error
	AdmitMaintenanceAttempt(context.Context, MaintenanceClaim, string, int, int, int, time.Time) error
	ReconcileMaintenanceAttempt(context.Context, MaintenanceClaim, string, AssessmentAttempt) error
	CompleteMaintenanceBatch(context.Context, MaintenanceClaim, OrganizationResult, string, time.Time) error
	MaintenanceCommand(context.Context, domain.OntologyMaintenanceCommand, time.Time, MaintenanceAudit) (MaintenanceRun, error)
	MaintenanceStatus(context.Context, time.Time) (MaintenanceStatus, error)
	ListMaintenanceRuns(context.Context, string, int) (MaintenanceRunPage, error)
}

type maintenanceClaimKey struct{}

func WithMaintenanceClaim(ctx context.Context, claim MaintenanceClaim) context.Context {
	return context.WithValue(ctx, maintenanceClaimKey{}, claim)
}

func MaintenanceClaimFromContext(ctx context.Context) (MaintenanceClaim, bool) {
	claim, ok := ctx.Value(maintenanceClaimKey{}).(MaintenanceClaim)
	return claim, ok
}

func MaintenanceFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrMaintenancePaused):
		return "maintenance_paused"
	case errors.Is(err, ErrMaintenanceDisabled):
		return "maintenance_disabled"
	case errors.Is(err, ErrWindowClosed):
		return "window_closed"
	case errors.Is(err, ErrConfiguration):
		return "configuration_invalid"
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, ErrAccounting):
		return "accounting_unavailable"
	case errors.Is(err, ErrAuditUnavailable):
		return "audit_unavailable"
	}
	return ""
}

func MaintenanceOutcomeState(outcome OrganizationOutcome, result OrganizationResult, failure string) string {
	if failure == "maintenance_paused" || failure == "maintenance_disabled" || failure == "lease_lost" || failure == "window_closed" || failure == "configuration_invalid" {
		return "pending"
	}
	if failure != "" || result.FailureCode != "" {
		return "failed"
	}
	for _, pair := range result.AmbiguousComparisons {
		if SourceKey(pair.Left) == SourceKey(outcome.Source) || SourceKey(pair.Right) == SourceKey(outcome.Source) {
			return "ambiguous"
		}
	}
	switch outcome.Status {
	case "organized", "unchanged":
		return "organized"
	case "ambiguous", "oversized":
		return "ambiguous"
	case "unavailable":
		return "unavailable"
	default:
		return "failed"
	}
}
