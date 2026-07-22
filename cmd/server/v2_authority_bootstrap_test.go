package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
)

func TestClassifyV2AuthorityActivatesWhenCompatibleMarkerPresent(t *testing.T) {
	store := &v2AuthorityStoreStub{marker: &domain.V2CompatibilityMarker{
		Status: domain.V2MigrationMarkerCompatible,
	}}

	bootstrap, err := classifyV2Authority(context.Background(), config.Config{}, store)

	require.NoError(t, err)
	require.Equal(t, v2AuthorityActive, bootstrap.Mode)
	require.True(t, bootstrap.DataPlaneAllowed)
	require.False(t, bootstrap.RequiresNeo4j)
	require.False(t, store.freshCalled)
}

func TestClassifyV2AuthorityRequiresMigrationWhenNeo4jConfiguredWithoutMarker(t *testing.T) {
	bootstrap, err := classifyV2Authority(context.Background(), config.Config{
		Neo4jURI:      "bolt://localhost:7687",
		Neo4jUser:     "neo4j",
		Neo4jPassword: "password",
	}, &v2AuthorityStoreStub{})

	require.NoError(t, err)
	require.Equal(t, v2AuthorityMigrationRequired, bootstrap.Mode)
	require.True(t, bootstrap.RequiresNeo4j)
	require.True(t, bootstrap.MigrationRequired)
	require.False(t, bootstrap.DataPlaneAllowed)
}

func TestClassifyV2AuthorityInitializesFreshWhenNoNeo4jAndPostgresEmpty(t *testing.T) {
	store := &v2AuthorityStoreStub{}

	bootstrap, err := classifyV2Authority(context.Background(), config.Config{}, store)

	require.NoError(t, err)
	require.Equal(t, v2AuthorityFresh, bootstrap.Mode)
	require.True(t, bootstrap.DataPlaneAllowed)
	require.True(t, store.freshCalled)
	require.Equal(t, migrationcontrol.DefaultCutoverMarkerVersion, store.freshInput.MarkerVersion)
	require.Equal(t, true, bootstrap.Marker.Metadata["fresh_install"])
}

func TestClassifyV2AuthorityFailsClosedForIncompatibleOrNonemptyNoNeo4j(t *testing.T) {
	_, err := classifyV2Authority(context.Background(), config.Config{}, &v2AuthorityStoreStub{
		marker: &domain.V2CompatibilityMarker{Status: domain.V2MigrationMarkerCorrupt},
	})
	require.ErrorIs(t, err, errV2AuthorityBlocked)

	_, err = classifyV2Authority(context.Background(), config.Config{}, &v2AuthorityStoreStub{
		freshErr: repository.ErrV2MigrationFreshInitBlocked,
	})
	require.ErrorIs(t, err, errV2AuthorityBlocked)
	require.ErrorContains(t, err, "no Neo4j migration source")
}

func TestLegacyMigrationDataPlaneStatusGateStaysClosedAfterCutover(t *testing.T) {
	status, err := (legacyMigrationDataPlaneStatusGate{
		inner: v2AuthorityStatusStub{status: &domain.V2MigrationControlStatus{
			State:            domain.V2MigrationStateCutOver,
			DataPlaneAllowed: true,
			ReadinessMessage: "compatible V2 migration marker present",
		}},
	}).Status(context.Background())

	require.NoError(t, err)
	require.False(t, status.DataPlaneAllowed)
	require.Contains(t, status.ReadinessMessage, "restart")
}

func TestCheckV2MigrationDataPlaneReadinessRequiresAllowedDataPlane(t *testing.T) {
	err := checkV2MigrationDataPlaneReadiness(context.Background(), v2AuthorityStatusStub{status: &domain.V2MigrationControlStatus{
		State:            domain.V2MigrationStateRequired,
		DataPlaneAllowed: false,
		ReadinessMessage: "migration_required",
	}})
	require.ErrorIs(t, err, errV2AuthorityBlocked)
	require.ErrorContains(t, err, "migration_required")

	err = checkV2MigrationDataPlaneReadiness(context.Background(), v2AuthorityStatusStub{status: &domain.V2MigrationControlStatus{
		State:            domain.V2MigrationStateCutOver,
		DataPlaneAllowed: true,
	}})
	require.NoError(t, err)
}

func TestCheckV2MigrationDataPlaneReadinessStaysClosedAfterCutoverUntilRestart(t *testing.T) {
	err := checkV2MigrationDataPlaneReadiness(context.Background(), legacyMigrationDataPlaneStatusGate{
		inner: v2AuthorityStatusStub{status: &domain.V2MigrationControlStatus{
			State:            domain.V2MigrationStateCutOver,
			DataPlaneAllowed: true,
		}},
	})

	require.ErrorIs(t, err, errV2AuthorityBlocked)
	require.ErrorContains(t, err, "restart")
}

func TestValidateLegacyMigrationNeo4jConfigOnlyRequiresNeo4jForMigrationBoot(t *testing.T) {
	err := validateLegacyMigrationNeo4jConfig(v2AuthorityBootstrap{
		Mode:             v2AuthorityActive,
		DataPlaneAllowed: true,
	}, config.Config{})
	require.NoError(t, err)

	err = validateLegacyMigrationNeo4jConfig(v2AuthorityBootstrap{
		Mode:              v2AuthorityMigrationRequired,
		RequiresNeo4j:     true,
		MigrationRequired: true,
	}, config.Config{})
	require.ErrorContains(t, err, "legacy migration requires NEO4J_URI")

	err = validateLegacyMigrationNeo4jConfig(v2AuthorityBootstrap{
		Mode:              v2AuthorityMigrationRequired,
		RequiresNeo4j:     true,
		MigrationRequired: true,
	}, config.Config{
		Neo4jURI:      "bolt://localhost:7687",
		Neo4jUser:     "neo4j",
		Neo4jPassword: "password",
	})
	require.NoError(t, err)
}

type v2AuthorityStoreStub struct {
	marker      *domain.V2CompatibilityMarker
	markerErr   error
	freshInput  repository.V2CommitFreshV2AuthorityInput
	freshErr    error
	freshCalled bool
}

type v2AuthorityStatusStub struct {
	status *domain.V2MigrationControlStatus
	err    error
}

func (s v2AuthorityStatusStub) Status(context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.status, nil
}

func (s *v2AuthorityStoreStub) GetLatestMarker(context.Context) (*domain.V2CompatibilityMarker, error) {
	if s.markerErr != nil {
		return nil, s.markerErr
	}
	return s.marker, nil
}

func (s *v2AuthorityStoreStub) CommitFreshV2Authority(_ context.Context, input repository.V2CommitFreshV2AuthorityInput) (*domain.V2CompatibilityMarker, error) {
	s.freshCalled = true
	s.freshInput = input
	if s.freshErr != nil {
		return nil, s.freshErr
	}
	if input.MarkerVersion == "" {
		return nil, errors.New("missing marker version")
	}
	s.marker = &domain.V2CompatibilityMarker{
		Status:   domain.V2MigrationMarkerCompatible,
		Version:  input.MarkerVersion,
		Metadata: map[string]any{"fresh_install": true},
	}
	return s.marker, nil
}
