package main

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/stretchr/testify/require"
)

func TestBuildDormantV2BootstrapDisabledByDefault(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(config.Config{V2BootMode: config.V2BootModeOff}, nil, nil)

	require.False(t, bootstrap.Enabled)
	require.False(t, bootstrap.AcceptsDataPlane)
	require.Empty(t, bootstrap.HealthChecks())
}

func TestBuildDormantV2BootstrapRegistersNonRoutingChecks(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(validDormantV2BootstrapConfig(), nil, nil)

	require.True(t, bootstrap.Enabled)
	require.False(t, bootstrap.AcceptsDataPlane)
	checks := bootstrap.HealthChecks()
	require.NotEmpty(t, checks)

	seen := map[string]bool{}
	for _, check := range checks {
		seen[check.Name] = true
	}
	require.True(t, seen["v2_provider_contract"])
	require.True(t, seen["v2_search_profile"])
	require.True(t, seen["v2_schema_index_profile"])
	require.True(t, seen["v2_queue_profile"])
	require.True(t, seen["v2_workers"])
	require.True(t, seen["v2_migration_state"])
}

func TestDormantV2MigrationCheckIsOptional(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(validDormantV2BootstrapConfig(), nil, nil)

	check := requireHealthCheck(t, bootstrap, "v2_migration_state")
	require.True(t, check.Optional)
	require.NoError(t, check.Check(context.Background()))
}

func TestDormantV2MigrationCheckReportsOptionalPendingState(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(validDormantV2BootstrapConfig(), nil, v2MigrationStatusStub{
		status: &domain.V2MigrationControlStatus{State: domain.V2MigrationStateRequired},
	})

	check := requireHealthCheck(t, bootstrap, "v2_migration_state")
	require.True(t, check.Optional)
	require.True(t, errors.Is(check.Check(context.Background()), errV2MigrationPending))
}

func TestDormantV2MigrationCheckBlocksWhenExplicitlyRequired(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.V2LegacyMigrationRequired = true
	bootstrap := buildDormantV2Bootstrap(cfg, nil, v2MigrationStatusStub{
		status: &domain.V2MigrationControlStatus{State: domain.V2MigrationStateRequired},
	})

	check := requireHealthCheck(t, bootstrap, "v2_migration_state")
	require.False(t, check.Optional)
	require.True(t, errors.Is(check.Check(context.Background()), errV2MigrationPending))
}

func TestDormantV2PGVectorDisabledIsOptionalDegraded(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.PGVectorExtensionRequired = false
	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	check := requireHealthCheck(t, bootstrap, "v2_pgvector")
	require.True(t, check.Optional)
	require.True(t, errors.Is(check.Check(context.Background()), errV2PGVectorDegraded))
}

func TestDormantV2ReadinessRejectsUnavailableWorkers(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.MemoryPlacementWorkerCount = 0
	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	err := requireHealthCheck(t, bootstrap, "v2_workers").Check(context.Background())

	require.True(t, errors.Is(err, errV2WorkersNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "MEMORY_PLACEMENT_WORKER_COUNT", validationErr.Field)
}

func TestDormantV2ReadinessRejectsMissingSchemaProfile(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.SearchDocumentFormatVersion = ""
	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	err := requireHealthCheck(t, bootstrap, "v2_schema_index_profile").Check(context.Background())

	require.True(t, errors.Is(err, errV2SchemaIndexNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "SEARCH_DOCUMENT_FORMAT_VERSION", validationErr.Field)
}

func TestDormantV2ReadinessRejectsInvalidQueueProfile(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.MemoryPlacementHeartbeatSeconds = cfg.MemoryPlacementLeaseSeconds
	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	err := requireHealthCheck(t, bootstrap, "v2_queue_profile").Check(context.Background())

	require.True(t, errors.Is(err, errV2QueueNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "MEMORY_PLACEMENT_HEARTBEAT_SECONDS", validationErr.Field)
}

func TestDormantV2ReadinessRejectsEmbeddingPollAtOrAboveLease(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.EmbeddingJobPollSeconds = cfg.EmbeddingJobLeaseSeconds
	bootstrap := buildDormantV2Bootstrap(cfg, nil, nil)

	err := requireHealthCheck(t, bootstrap, "v2_queue_profile").Check(context.Background())

	require.True(t, errors.Is(err, errV2QueueNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "EMBEDDING_JOB_POLL_SECONDS", validationErr.Field)
}

func validDormantV2BootstrapConfig() config.Config {
	return config.Config{
		V2BootMode:                       config.V2BootModeDormant,
		AIAPIURL:                         "https://example.com/v1",
		AIAPIKey:                         "sk-test",
		AIEmbeddingModel:                 "text-embedding-3-small",
		AIEmbeddingDimensions:            1536,
		AIReviewerModel:                  "reviewer-model",
		AIVerifierModel:                  "verifier-model",
		SearchDocumentFormatVersion:      "search-doc-v1",
		EmbeddingNormalizationVersion:    "embedding-norm-v1",
		PredicateRegistryVersion:         "predicate-registry-v1",
		PGVectorExtensionRequired:        true,
		PGVectorDistance:                 "cosine",
		PGVectorANNStrategy:              "auto",
		PGVectorHNSWM:                    16,
		PGVectorHNSWEFConstruction:       64,
		PGVectorIndexBuildMaxConcurrency: 1,
		MemoryPlacementWorkerCount:       1,
		MemoryPlacementLeaseSeconds:      30,
		MemoryPlacementHeartbeatSeconds:  10,
		MemoryPlacementPollSeconds:       5,
		MemoryPlacementMaxAttempts:       3,
		EmbeddingWorkerCount:             2,
		EmbeddingBatchSize:               64,
		EmbeddingJobLeaseSeconds:         30,
		EmbeddingJobPollSeconds:          5,
		EmbeddingJobMaxAttempts:          3,
		EmbeddingJobRetryMaxSeconds:      300,
		EmbeddingPendingStaleSeconds:     300,
	}
}

func requireHealthCheck(t *testing.T, bootstrap dormantV2Bootstrap, name string) internalhttp.HealthCheck {
	t.Helper()
	for _, check := range bootstrap.HealthChecks() {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("health check %q not found", name)
	return internalhttp.HealthCheck{}
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
