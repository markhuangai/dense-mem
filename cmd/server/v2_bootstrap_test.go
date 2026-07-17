package main

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/stretchr/testify/require"
)

func TestBuildDormantV2BootstrapDisabledByDefault(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(config.Config{V2BootMode: config.V2BootModeOff}, nil)

	require.False(t, bootstrap.Enabled)
	require.False(t, bootstrap.AcceptsDataPlane)
	require.Empty(t, bootstrap.HealthChecks())
}

func TestBuildDormantV2BootstrapRegistersNonRoutingChecks(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(validDormantV2BootstrapConfig(), nil)

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
}

func TestDormantV2PGVectorDisabledIsOptionalDegraded(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.PGVectorExtensionRequired = false
	bootstrap := buildDormantV2Bootstrap(cfg, nil)

	check := requireHealthCheck(t, bootstrap, "v2_pgvector")
	require.True(t, check.Optional)
	require.True(t, errors.Is(check.Check(context.Background()), errV2PGVectorDegraded))
}

func TestDormantV2ReadinessRejectsUnavailableWorkers(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.MemoryPlacementWorkerCount = 0
	bootstrap := buildDormantV2Bootstrap(cfg, nil)

	err := requireHealthCheck(t, bootstrap, "v2_workers").Check(context.Background())

	require.True(t, errors.Is(err, errV2WorkersNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "MEMORY_PLACEMENT_WORKER_COUNT", validationErr.Field)
}

func TestDormantV2ReadinessRejectsMissingSchemaProfile(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.SearchDocumentFormatVersion = ""
	bootstrap := buildDormantV2Bootstrap(cfg, nil)

	err := requireHealthCheck(t, bootstrap, "v2_schema_index_profile").Check(context.Background())

	require.True(t, errors.Is(err, errV2SchemaIndexNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "SEARCH_DOCUMENT_FORMAT_VERSION", validationErr.Field)
}

func TestDormantV2ReadinessRejectsInvalidQueueProfile(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.MemoryPlacementHeartbeatSeconds = cfg.MemoryPlacementLeaseSeconds
	bootstrap := buildDormantV2Bootstrap(cfg, nil)

	err := requireHealthCheck(t, bootstrap, "v2_queue_profile").Check(context.Background())

	require.True(t, errors.Is(err, errV2QueueNotReady), "err=%v", err)
	var validationErr *config.ValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "MEMORY_PLACEMENT_HEARTBEAT_SECONDS", validationErr.Field)
}

func TestDormantV2ReadinessRejectsEmbeddingPollAtOrAboveLease(t *testing.T) {
	cfg := validDormantV2BootstrapConfig()
	cfg.EmbeddingJobPollSeconds = cfg.EmbeddingJobLeaseSeconds
	bootstrap := buildDormantV2Bootstrap(cfg, nil)

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
