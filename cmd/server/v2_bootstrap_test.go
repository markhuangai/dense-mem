package main

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildDormantV2BootstrapDisabledByDefault(t *testing.T) {
	bootstrap := buildDormantV2Bootstrap(config.Config{V2BootMode: config.V2BootModeOff}, nil)

	require.False(t, bootstrap.Enabled)
	require.False(t, bootstrap.AcceptsDataPlane)
	require.Empty(t, bootstrap.HealthChecks())
}

func TestBuildDormantV2BootstrapRegistersNonRoutingChecks(t *testing.T) {
	cfg := config.Config{
		V2BootMode:                    config.V2BootModeDormant,
		AIAPIURL:                      "https://example.com/v1",
		AIAPIKey:                      "sk-test",
		AIEmbeddingModel:              "text-embedding-3-small",
		AIEmbeddingDimensions:         1536,
		AIReviewerModel:               "reviewer-model",
		AIVerifierModel:               "verifier-model",
		SearchDocumentFormatVersion:   "search-doc-v1",
		EmbeddingNormalizationVersion: "embedding-norm-v1",
		PredicateRegistryVersion:      "predicate-registry-v1",
		PGVectorExtensionRequired:     false,
	}

	bootstrap := buildDormantV2Bootstrap(cfg, nil)

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
	require.True(t, seen["v2_workers"])
	require.True(t, seen["v2_migration_state"])
}

func TestDormantV2MigrationCheckIsOptional(t *testing.T) {
	cfg := config.Config{
		V2BootMode:                    config.V2BootModeDormant,
		AIAPIURL:                      "https://example.com/v1",
		AIAPIKey:                      "sk-test",
		AIEmbeddingModel:              "text-embedding-3-small",
		AIEmbeddingDimensions:         1536,
		AIReviewerModel:               "reviewer-model",
		AIVerifierModel:               "verifier-model",
		SearchDocumentFormatVersion:   "search-doc-v1",
		EmbeddingNormalizationVersion: "embedding-norm-v1",
		PredicateRegistryVersion:      "predicate-registry-v1",
	}
	bootstrap := buildDormantV2Bootstrap(cfg, nil)

	var found bool
	for _, check := range bootstrap.HealthChecks() {
		if check.Name != "v2_migration_state" {
			continue
		}
		found = true
		require.True(t, check.Optional)
		require.True(t, errors.Is(check.Check(context.Background()), errV2MigrationPending))
	}
	require.True(t, found)
}
