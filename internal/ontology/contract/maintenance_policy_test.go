package contract

import (
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceWindowBoundaries(t *testing.T) {
	policy := domain.OntologyMaintenanceConfig{CadenceHours: 12, StartTimeLocal: "03:00", Timezone: "UTC", MaxConcurrency: 1, InputTokens: 250000, OutputTokens: 100000}
	now := time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC)
	start, end, err := MaintenanceWindowBounds(policy, now, nil)
	require.NoError(t, err)
	require.Equal(t, now.Add(-time.Hour), start)
	require.Equal(t, start.Add(12*time.Hour), end)
	previous := &MaintenanceWindow{StartsAt: start, EndsAt: end}
	policy.CadenceHours = 24
	start, _, err = MaintenanceWindowBounds(policy, end, previous)
	require.NoError(t, err)
	require.Equal(t, end, start)
	policy.StartTimeLocal = "04:00"
	start, _, err = MaintenanceWindowBounds(policy, end, previous)
	require.NoError(t, err)
	require.True(t, start.IsZero(), "a changed schedule must not overlap the previous window")
	start, end, err = MaintenanceWindowBounds(policy, now.Add(72*time.Hour), previous)
	require.NoError(t, err)
	require.Equal(t, 24*time.Hour, end.Sub(start))
	require.True(t, start.After(previous.EndsAt), "missed windows must not be funded")
	policy.Timezone = "America/New_York"
	policy.StartTimeLocal = "03:00"
	start, end, err = MaintenanceWindowBounds(policy, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), nil)
	require.NoError(t, err)
	require.Equal(t, 3, start.In(mustLocation(t, policy.Timezone)).Hour())
	require.Equal(t, 3, end.In(mustLocation(t, policy.Timezone)).Hour())
	policy.Timezone = "invalid/timezone"
	_, _, err = MaintenanceWindowBounds(policy, now, nil)
	require.ErrorIs(t, err, ErrInvalid)
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	require.NoError(t, err)
	return location
}

func TestMaintenanceCommandsAndOutcomes(t *testing.T) {
	command, err := PrepareMaintenanceCommand(domain.OntologyMaintenanceCommand{OperationKey: "bounded", Action: "run"})
	require.NoError(t, err)
	require.Equal(t, 1, command.MaxBatches)
	for _, command := range []domain.OntologyMaintenanceCommand{{OperationKey: "invalid", Action: "run", MaxBatches: 101}, {OperationKey: "invalid", Action: "pause", MaxBatches: 1}, {OperationKey: "invalid", Action: "retry"}, {Action: "run"}} {
		_, err := PrepareMaintenanceCommand(command)
		require.ErrorIs(t, err, ErrInvalid)
	}
	source := SourceHandle{Kind: EvidenceSource, ID: "source", Version: 1}
	result := OrganizationResult{AmbiguousComparisons: []AmbiguousComparison{{Left: source}}}
	require.Equal(t, "ambiguous", MaintenanceOutcomeState(OrganizationOutcome{Source: source, Status: "organized"}, result, "", false))
	require.Equal(t, "budget_deferred", MaintenanceOutcomeState(OrganizationOutcome{}, OrganizationResult{}, "budget_deferred", false))
	require.Equal(t, "pending", MaintenanceOutcomeState(OrganizationOutcome{}, OrganizationResult{}, "budget_deferred", true))
	require.Equal(t, "failed", MaintenanceOutcomeState(OrganizationOutcome{}, OrganizationResult{}, "accounting_unavailable", true))
}

func TestMaintenanceAllowanceValidationAndHistoricalHash(t *testing.T) {
	grant := domain.OntologyMaintenanceCommand{OperationKey: "grant", Action: "allowance", WindowID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OutputTokens: 50000}
	_, err := PrepareMaintenanceCommand(grant)
	require.NoError(t, err)
	for _, change := range []func(*domain.OntologyMaintenanceCommand){
		func(c *domain.OntologyMaintenanceCommand) { c.WindowID = "" },
		func(c *domain.OntologyMaintenanceCommand) { c.OutputTokens = 0 },
		func(c *domain.OntologyMaintenanceCommand) { c.InputTokens = -1 },
		func(c *domain.OntologyMaintenanceCommand) { c.OutputTokens = MaxAdditionalTokens + 1 },
		func(c *domain.OntologyMaintenanceCommand) { c.MaxBatches = 1 },
		func(c *domain.OntologyMaintenanceCommand) { c.RetryRunID = c.WindowID },
		func(c *domain.OntologyMaintenanceCommand) { c.Action = "run" },
	} {
		invalid := grant
		change(&invalid)
		_, err := PrepareMaintenanceCommand(invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	hash, err := MaintenanceCommandHash(domain.OntologyMaintenanceCommand{OperationKey: "legacy-run", Action: "run", MaxBatches: 1})
	require.NoError(t, err)
	require.Equal(t, "sha256:1ea5e21f36698df9a0be8a879cfb26b2f5826703fa02b8dd7288b0051f10016d", hash)
}

func TestMaintenanceCompletionRetainsOnlyUnresolvedFailureReasons(t *testing.T) {
	status, reason := MaintenanceCompletion(false, "maintenance_paused")
	require.Equal(t, "completed", status)
	require.Empty(t, reason)
	status, reason = MaintenanceCompletion(true, "provider_unavailable")
	require.Equal(t, "incomplete", status)
	require.Equal(t, "provider_unavailable", reason)
	status, reason = MaintenanceCompletion(true, "")
	require.Equal(t, "incomplete", status)
	require.Empty(t, reason)
}
