package contract

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const MaintenancePageSize = 100
const MaxManualBatches = 100

var (
	ErrMaintenancePaused   = errors.New("ontology maintenance paused")
	ErrMaintenanceDisabled = errors.New("ontology maintenance disabled")
	ErrBudgetDeferred      = errors.New("ontology maintenance budget deferred")
	ErrLeaseLost           = errors.New("ontology maintenance lease lost")
	ErrAccounting          = errors.New("ontology maintenance accounting unavailable")
)

type MaintenanceWindow struct {
	ID             string                           `json:"id"`
	StartsAt       time.Time                        `json:"starts_at"`
	EndsAt         time.Time                        `json:"ends_at"`
	Policy         domain.OntologyMaintenanceConfig `json:"policy"`
	ChargedInput   int64                            `json:"charged_input_tokens"`
	ChargedOutput  int64                            `json:"charged_output_tokens"`
	ReportedInput  int64                            `json:"reported_input_tokens"`
	ReportedOutput int64                            `json:"reported_output_tokens"`
	ReservedInput  int64                            `json:"reserved_input_tokens"`
	ReservedOutput int64                            `json:"reserved_output_tokens"`
	Overrun        bool                             `json:"overrun"`
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
}

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
	Eligible       int64 `json:"eligible"`
	Organized      int64 `json:"organized"`
	Pending        int64 `json:"pending"`
	Ambiguous      int64 `json:"ambiguous"`
	Failed         int64 `json:"failed"`
	BudgetDeferred int64 `json:"budget_deferred"`
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
}

type MaintenanceRunPage struct {
	Runs       []MaintenanceRun `json:"runs"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type MaintenanceRepository interface {
	SeedDefinitions(context.Context, string, SeedInput) (SeedResult, error)
	ListRecords(context.Context, string, Kind, string, int) (Page, error)
	EnsureMaintenanceWindow(context.Context, domain.OntologyMaintenanceConfig, time.Time) (*MaintenanceWindow, error)
	ClaimMaintenanceTurn(context.Context, string, time.Time, time.Duration) (*MaintenanceTurn, error)
	DiscoverMaintenance(context.Context, MaintenanceTurn, int) error
	ClaimMaintenanceBatch(context.Context, MaintenanceTurn, string, time.Time) (*MaintenanceClaim, error)
	ReleaseMaintenanceTurn(context.Context, MaintenanceTurn) error
	RecordMaintenanceFailure(context.Context, string, string, time.Time) error
	ReserveMaintenanceAttempt(context.Context, MaintenanceClaim, string, int, int, int, time.Time) error
	ReconcileMaintenanceAttempt(context.Context, MaintenanceClaim, string, AssessmentAttempt) error
	CompleteMaintenanceBatch(context.Context, MaintenanceClaim, OrganizationResult, string, time.Time) error
	MaintenanceCommand(context.Context, domain.OntologyMaintenanceCommand, time.Time) (MaintenanceRun, error)
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
	case errors.Is(err, ErrBudgetDeferred):
		return "budget_deferred"
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, ErrAccounting):
		return "accounting_unavailable"
	}
	return ""
}

func MaintenanceOutcomeState(outcome OrganizationOutcome, result OrganizationResult, failure string) string {
	if failure == "maintenance_paused" || failure == "maintenance_disabled" {
		return "pending"
	}
	if failure == "budget_deferred" || failure == "lease_lost" {
		return "budget_deferred"
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
