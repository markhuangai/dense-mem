package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestBuildDormantV2BootstrapDisabledByDefault(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(config.Config{V2BootMode: config.V2BootModeOff}, nil, nil)

	require.False(t, bootstrap.Enabled)
	require.False(t, bootstrap.AcceptsDataPlane)
	require.Empty(t, bootstrap.HealthChecks())
}

func TestDefaultCatalogV2DependenciesFollowBootGate(t *testing.T) {
	semantic := repository.NewV2SemanticRepository(nil, nil)

	disabled := buildDefaultCatalogV2Dependencies(dormantV2Bootstrap{}, semantic)
	require.Nil(t, disabled.Evaluation)
	require.Nil(t, disabled.Communities)

	dormant := buildDefaultCatalogV2Dependencies(dormantV2Bootstrap{Enabled: true, Mode: config.V2BootModeDormant}, semantic)
	require.Nil(t, dormant.Evaluation)
	require.False(t, dormant.EvaluationEnabled)
	require.Nil(t, dormant.Communities)

	uat := buildDefaultCatalogV2Dependencies(dormantV2Bootstrap{Enabled: true, Mode: config.V2BootModeUAT}, semantic)
	require.NotNil(t, uat.Evaluation)
	require.True(t, uat.EvaluationEnabled)
	require.NotNil(t, uat.Communities)
}

func TestBuildDormantV2BootstrapRegistersNonRoutingChecks(t *testing.T) {
	cfg := config.Config{
		V2BootMode: config.V2BootModeDormant,
	}

	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	require.True(t, bootstrap.Enabled)
	require.False(t, bootstrap.AcceptsDataPlane)
	checks := bootstrap.HealthChecks()
	require.NotEmpty(t, checks)

	seen := map[string]bool{}
	for _, check := range checks {
		seen[check.Name] = true
	}
	require.True(t, seen["v2_postgres_topology"])
	require.True(t, seen["v2_pgvector"])
	require.True(t, seen["v2_workers"])
	require.True(t, seen["v2_migration_state"])
}

func TestDormantV2MigrationCheckIsOptional(t *testing.T) {
	cfg := config.Config{
		V2BootMode: config.V2BootModeDormant,
	}
	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	var found bool
	for _, check := range bootstrap.HealthChecks() {
		if check.Name != "v2_migration_state" {
			continue
		}
		found = true
		require.True(t, check.Optional)
		require.NoError(t, check.Check(context.Background()))
	}
	require.True(t, found)
}

func TestDormantV2MigrationCheckBlocksWhenExplicitlyRequired(t *testing.T) {
	cfg := config.Config{
		V2BootMode:                config.V2BootModeDormant,
		V2LegacyMigrationRequired: true,
	}
	bootstrap := buildDormantV2Bootstrap(cfg, nil, v2MigrationStatusStub{
		status: &domain.V2MigrationControlStatus{State: domain.V2MigrationStateRequired},
	})

	var found bool
	for _, check := range bootstrap.HealthChecks() {
		if check.Name != "v2_migration_state" {
			continue
		}
		found = true
		require.False(t, check.Optional)
		require.True(t, errors.Is(check.Check(context.Background()), errV2MigrationPending))
	}
	require.True(t, found)
}

func TestDormantV2MigrationCheckPassesAfterCutoverMarker(t *testing.T) {
	cfg := config.Config{
		V2BootMode:                config.V2BootModeDormant,
		V2LegacyMigrationRequired: true,
	}
	err := checkV2MigrationState(context.Background(), cfg, v2MigrationStatusStub{
		status: &domain.V2MigrationControlStatus{State: domain.V2MigrationStateCutOver},
	})
	require.NoError(t, err)
}

func TestV2SearchReadinessCheckReportsRepositoryFailure(t *testing.T) {
	err := checkV2SearchReadiness(context.Background(), v2SearchReadinessStub{
		err: repository.ErrV2SearchContractMismatch,
	})

	require.True(t, errors.Is(err, repository.ErrV2SearchContractMismatch))
}

func TestV2SearchReadinessCheckReportsReasons(t *testing.T) {
	err := checkV2SearchReadiness(context.Background(), v2SearchReadinessStub{
		readiness: &repository.V2SearchReadiness{
			Ready: false,
			Reasons: []repository.V2SearchReadinessReason{{
				Code:    "embedding_backlog_pending",
				Message: "2 embedding jobs are queued or processing for active search contract",
			}},
		},
	})

	require.True(t, errors.Is(err, repository.ErrV2SearchContractMismatch))
	require.Contains(t, err.Error(), "2 embedding jobs")
}

func TestV2UATPlacementLeaseCoversProviderAndCommitTimeouts(t *testing.T) {
	require.Equal(t, 5*time.Minute, v2UATPlacementLease(60, 10))
	require.Equal(t, 9*time.Minute, v2UATPlacementLease(120, 30))
	require.Equal(t, 5*time.Minute, v2UATPlacementLease(0, 0))
}

type v2MigrationStatusStub struct {
	status *domain.V2MigrationControlStatus
	err    error
}

func (s v2MigrationStatusStub) Status(context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.status, nil
}

type v2SearchReadinessStub struct {
	readiness *repository.V2SearchReadiness
	err       error
}

func (s v2SearchReadinessStub) CheckSearchReadiness(context.Context) (*repository.V2SearchReadiness, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.readiness, nil
}
