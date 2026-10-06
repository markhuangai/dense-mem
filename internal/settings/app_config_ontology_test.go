package settings

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestOntologySettingsDefaultsValidationAndFreshReads(t *testing.T) {
	now := time.Now().UTC()
	repo := newAppConfigRepoStub(now, map[string]string{domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano)})
	service := NewAppConfigService(repo, nil)
	ctx := context.Background()
	initial, err := service.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, initial.Enabled)
	require.Equal(t, 12, initial.CadenceHours)
	require.Equal(t, "03:00", initial.StartTimeLocal)
	require.EqualValues(t, 250000, initial.InputTokens)
	require.EqualValues(t, 100000, initial.OutputTokens)
	require.Equal(t, 1, initial.MaxConcurrency)
	for key, value := range map[string]string{domain.AppConfigOntologyCadenceHours: "18", domain.AppConfigOntologyStartTime: "25:00", domain.AppConfigOntologyConcurrency: "9", domain.AppConfigOntologyInputTokens: "0", domain.AppConfigOntologyOutputTokens: "-1", domain.AppConfigOntologyModel: "model\nheader", "unknown": "true"} {
		_, err := service.UpdateOntologyMaintenanceSettings(ctx, map[string]string{key: value}, "control", "", "")
		require.ErrorIs(t, err, ErrInvalidAppConfig)
	}
	_, err = service.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyCadenceHours: "24", domain.AppConfigOntologyModel: "operator-model", domain.AppConfigOntologyStartTime: "3:00"}, "control", "", "")
	require.NoError(t, err)
	_, err = service.UpdateGeneralSettings(ctx, map[string]string{domain.AppConfigTimezone: "America/New_York"}, "control", "", "")
	require.NoError(t, err)
	updated, err := service.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	require.Equal(t, 24, updated.CadenceHours)
	require.Equal(t, "03:00", updated.StartTimeLocal)
	require.Equal(t, "operator-model", updated.Model)
	require.Equal(t, "America/New_York", updated.Timezone)
	_, err = repo.UpdateValues(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour))
	require.NoError(t, err)
	updated, err = service.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, updated.Enabled, "disable must bypass the settings cache")
}
