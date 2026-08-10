package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTelemetryCatalogIsOrderedAndComplete(t *testing.T) {
	seen := make(map[string]struct{}, len(orderedTelemetryCatalog))
	for _, entry := range orderedTelemetryCatalog {
		require.NotEmpty(t, entry.ID)
		require.NotEmpty(t, entry.Source, entry.ID)
		require.NotEmpty(t, entry.SourceKind, entry.ID)
		require.NotEmpty(t, entry.Audience, entry.ID)
		require.NotEmpty(t, entry.SupportedScopes, entry.ID)
		require.NotEmpty(t, entry.RuntimePrerequisite, entry.ID)
		require.NotEmpty(t, entry.ParentActivitySource, entry.ID)
		require.NotEmpty(t, entry.ZeroPolicy, entry.ID)
		require.NotEmpty(t, entry.Presentations, entry.ID)
		_, duplicate := seen[entry.ID]
		require.False(t, duplicate, "duplicate telemetry catalog ID %s", entry.ID)
		seen[entry.ID] = struct{}{}
	}

	system := TelemetryScope{Type: "system"}
	teamID := uuid.New()
	profileID := uuid.New()
	scopes := []TelemetryScope{
		system,
		{Type: "team", TeamID: &teamID},
		{Type: "profile", TeamID: &teamID, ProfileID: &profileID},
	}
	for _, scope := range scopes {
		groups := [][]telemetryQuerySpec{
			telemetryWindowedCardSpecsForAudience(scope, nil, "1h", false),
			telemetryWindowedCardSpecsForAudience(scope, nil, "1h", true),
			telemetryCurrentCardSpecsForAudience(scope, nil, false),
			telemetryCurrentCardSpecsForAudience(scope, nil, true),
			telemetryActivitySeriesSpecsForAudience("", "1m", false),
			telemetryActivitySeriesSpecsForAudience("", "1m", true),
		}
		for _, group := range groups {
			for _, spec := range group {
				require.Equal(t, spec.ID, spec.Catalog.ID)
				require.NotEmpty(t, spec.Catalog.Source, spec.ID)
				require.NotEmpty(t, spec.Catalog.SourceKind, spec.ID)
				require.NotEmpty(t, spec.Catalog.Audience, spec.ID)
				require.NotEmpty(t, spec.Catalog.SupportedScopes, spec.ID)
				require.NotEmpty(t, spec.Catalog.RuntimePrerequisite, spec.ID)
				require.NotEmpty(t, spec.Catalog.ParentActivitySource, spec.ID)
				require.NotEmpty(t, spec.Catalog.ZeroPolicy, spec.ID)
				require.NotEmpty(t, spec.Catalog.Presentations, spec.ID)
			}
		}
	}

	assessorTeam := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(scopes[1], nil, "1h", true), "avg_assessor_duration")
	require.Nil(t, assessorTeam)
	assessorSystem := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(system, nil, "1h", true), "avg_assessor_duration")
	require.NotNil(t, assessorSystem)
	require.False(t, telemetryScopeUnsupported(assessorSystem.ID, system))
	embeddingProfile := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(scopes[2], nil, "1h", false), "embedding_requests")
	require.NotNil(t, embeddingProfile)
	require.True(t, telemetryScopeUnsupported(embeddingProfile.ID, scopes[2]))
	embeddingTeam := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(scopes[1], nil, "1h", false), "embedding_requests")
	require.NotNil(t, embeddingTeam)
	require.False(t, telemetryScopeUnsupported(embeddingTeam.ID, scopes[1]))
	embeddingSeries := telemetryQuerySpecByID(telemetryActivitySeriesSpecsForAudience("", "1m", false), "embedding_requests")
	require.NotNil(t, embeddingSeries)
	require.True(t, telemetryScopeUnsupported(embeddingSeries.ID, scopes[2]))
	profileCost := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(scopes[2], nil, "1h", true), "embedding_cost_usd")
	require.NotNil(t, profileCost)
	require.True(t, telemetryScopeUnsupported(profileCost.ID, scopes[2]))
	conflictProfile := telemetryQuerySpecByID(telemetryActivitySeriesSpecsForAudience("", "1m", false), "conflict_review_duration")
	require.NotNil(t, conflictProfile)
	require.True(t, telemetryScopeUnsupported(conflictProfile.ID, scopes[2]))
}

func TestEmbeddingErrorTelemetryCountsCanonicalSeriesOnly(t *testing.T) {
	const canonicalCodeMatcher = `code=~"provider_rate_limited|provider_timeout|provider_network_error|provider_server_error|provider_quota_exhausted|provider_authentication_failed|provider_permission_denied|provider_contract_rejected|provider_response_invalid|embedding_input_rejected|embedding_contract_mismatch|unknown_embedding_failure|stale|lease_lost"`
	card := telemetryQuerySpecByID(telemetryWindowedCardSpecs(TelemetryScope{}, nil, "1h"), "embedding_errors")
	require.Contains(t, card.Query, canonicalCodeMatcher)
	series := telemetryQuerySpecByID(telemetryActivitySeriesSpecs("", "1m"), "embedding_errors")
	require.Contains(t, series.Query, canonicalCodeMatcher)
}
