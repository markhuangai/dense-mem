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
	require.NotNil(t, assessorTeam)
	require.True(t, telemetryScopeUnsupported(assessorTeam.ID, scopes[1]))
	require.False(t, telemetryScopeUnsupported(assessorTeam.ID, system))
	profileCost := telemetryQuerySpecByID(telemetryWindowedCardSpecsForAudience(scopes[2], nil, "1h", true), "embedding_cost_usd")
	require.NotNil(t, profileCost)
	require.True(t, telemetryScopeUnsupported(profileCost.ID, scopes[2]))
}
