package settings

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestOntologySettingsNumericProjectionPreservesBounds(t *testing.T) {
	entries := map[string]domain.AppConfigEntry{}
	for key, value := range map[string]string{domain.AppConfigTopicMaintenanceCadenceHours: "24", domain.AppConfigTopicMaintenanceConcurrency: "8", domain.AppConfigTopicMaintenanceContextWindow: "1000000000"} {
		entries[key] = domain.AppConfigEntry{Value: value}
	}
	settings, err := topicMaintenanceConfigFromEntries(entries, "UTC")
	require.NoError(t, err)
	require.Equal(t, 24, settings.Effective.CadenceHours)
	require.Equal(t, 8, settings.Effective.MaxConcurrency)
	require.EqualValues(t, 1000000000, settings.Effective.ContextWindowTokens)
	for _, key := range []string{domain.AppConfigTopicMaintenanceCadenceHours, domain.AppConfigTopicMaintenanceConcurrency} {
		before := entries[key]
		entries[key] = domain.AppConfigEntry{Value: "4294967320"}
		_, err := topicMaintenanceConfigFromEntries(entries, "UTC")
		require.ErrorIs(t, err, ErrInvalidAppConfig)
		entries[key] = before
	}
}

func TestOntologyTeamConcurrencySettings(t *testing.T) {
	const team = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	entries := map[string]domain.AppConfigEntry{}
	settings, err := topicMaintenanceConfigFromEntries(entries, "UTC")
	require.NoError(t, err)
	require.Equal(t, domain.DefaultTopicTeamConcurrency, settings.Effective.TeamMaxConcurrency)
	require.Empty(t, settings.Effective.TeamConcurrencyOverrides)
	entries[domain.AppConfigTopicMaintenanceConcurrency] = domain.AppConfigEntry{Value: "4"}
	entries[domain.AppConfigTopicMaintenanceTeamConcurrency] = domain.AppConfigEntry{Value: "2"}
	entries[domain.AppConfigTopicMaintenanceTeamOverrides] = domain.AppConfigEntry{Value: `[{"team_id":"` + team + `","max_concurrency":8}]`}
	settings, err = topicMaintenanceConfigFromEntries(entries, "UTC")
	require.NoError(t, err)
	require.Equal(t, 4, settings.Effective.TeamConcurrency(team))
	require.Equal(t, 2, settings.Effective.TeamConcurrency("other"))
	for _, raw := range []string{"null", "{}", "[] []", strings.Repeat(" ", 16385) + "[]", `[{"team_id":"unknown","max_concurrency":2}]`, `[{"team_id":"` + team + `","max_concurrency":0}]`, `[{"team_id":"` + team + `","max_concurrency":9}]`, `[{"team_id":"` + team + `","max_concurrency":1.5}]`, `[{"team_id":"` + team + `","max_concurrency":2,"unknown":1}]`, `[{"team_id":"` + team + `","team_id":"` + team + `","max_concurrency":2}]`, fmt.Sprintf(`[{"team_id":%q,"max_concurrency":2},{"team_id":%q,"max_concurrency":3}]`, team, team)} {
		_, err := parseTopicTeamOverrides(raw)
		require.ErrorIs(t, err, ErrInvalidAppConfig, raw)
	}
	_, err = normalizeTopicMaintenanceConfigValues(map[string]string{domain.AppConfigTopicMaintenanceTeamConcurrency: "9"})
	require.ErrorIs(t, err, ErrInvalidAppConfig)
	values, err := normalizeTopicMaintenanceConfigValues(map[string]string{domain.AppConfigTopicMaintenanceTeamOverrides: "[]"})
	require.NoError(t, err)
	require.Equal(t, "[]", values[domain.AppConfigTopicMaintenanceTeamOverrides])
	settings.Effective.TeamConcurrencyOverrides = nil
	settings.Effective.TeamMaxConcurrency = 0
	require.Equal(t, 1, settings.Effective.TeamConcurrency(team), "historical windows retain serial behavior")
}

func TestOntologySettingsDefaultsValidationAndFreshReads(t *testing.T) {
	now := time.Now().UTC()
	repo := newAppConfigRepoStub(now, map[string]string{domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano)})
	audit := &appConfigAuditStub{}
	service := NewAppConfigService(repo, audit)
	ctx := context.Background()
	initial, err := service.TopicMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, initial.Enabled)
	require.Equal(t, 12, initial.CadenceHours)
	require.Equal(t, "03:00", initial.StartTimeLocal)
	require.Zero(t, initial.ContextWindowTokens)
	require.Equal(t, 1, initial.MaxConcurrency)
	for key, value := range map[string]string{domain.AppConfigTopicMaintenanceCadenceHours: "18", domain.AppConfigTopicMaintenanceStartTime: "25:00", domain.AppConfigTopicMaintenanceConcurrency: "9", domain.AppConfigTopicMaintenanceContextWindow: "0", domain.AppConfigTopicMaintenanceInputTokens: "250000", domain.AppConfigTopicMaintenanceOutputTokens: "100000", domain.AppConfigTopicMaintenanceModel: "model\nheader", "unknown": "true"} {
		_, err := service.UpdateTopicMaintenanceSettings(ctx, map[string]string{key: value}, "control", "", "")
		require.ErrorIs(t, err, ErrInvalidAppConfig)
	}
	_, err = service.UpdateTopicMaintenanceSettings(ctx, map[string]string{domain.AppConfigTopicMaintenanceEnabled: "true", domain.AppConfigTopicMaintenanceCadenceHours: "24", domain.AppConfigTopicMaintenanceModel: "operator-model", domain.AppConfigTopicMaintenanceStartTime: "3:00"}, "control", "", "")
	require.NoError(t, err)
	require.Len(t, audit.entries, 1)
	require.Equal(t, "topic_maintenance", audit.entries[0].EntityID)
	require.Equal(t, initial.SettingsVersion, audit.entries[0].BeforePayload["update_time"])
	require.Contains(t, audit.entries[0].AfterPayload["items"], map[string]string{"key": domain.AppConfigTopicMaintenanceEnabled, "value": "true"})
	_, err = service.UpdateGeneralSettings(ctx, map[string]string{domain.AppConfigTimezone: "America/New_York"}, "control", "", "")
	require.NoError(t, err)
	updated, err := service.TopicMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	require.Equal(t, 24, updated.CadenceHours)
	require.Equal(t, "03:00", updated.StartTimeLocal)
	require.Equal(t, "operator-model", updated.Model)
	require.Equal(t, "America/New_York", updated.Timezone)
	_, err = repo.UpdateValues(ctx, map[string]string{domain.AppConfigTopicMaintenanceEnabled: "false"}, now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour))
	require.NoError(t, err)
	updated, err = service.TopicMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	require.False(t, updated.Enabled, "disable must bypass the settings cache")
}

func TestOntologyContextSettingsPreserveRetiredSavedValues(t *testing.T) {
	entries := map[string]domain.AppConfigEntry{
		domain.AppConfigTopicMaintenanceInputTokens:  {Value: "invalid historical input"},
		domain.AppConfigTopicMaintenanceOutputTokens: {Value: "100"},
	}
	settings, err := topicMaintenanceConfigFromEntries(entries, "UTC")
	require.NoError(t, err)
	require.Zero(t, settings.Effective.ContextWindowTokens)
	for _, item := range settings.Items {
		require.NotEqual(t, domain.AppConfigTopicMaintenanceInputTokens, item.Key)
		require.NotEqual(t, domain.AppConfigTopicMaintenanceOutputTokens, item.Key)
	}
	for _, raw := range []string{"0", "-1", "1.5", "1000000001", "9223372036854775808"} {
		_, err := normalizeTopicMaintenanceConfigValues(map[string]string{domain.AppConfigTopicMaintenanceContextWindow: raw})
		require.ErrorIs(t, err, ErrInvalidAppConfig)
	}
	values, err := normalizeTopicMaintenanceConfigValues(map[string]string{domain.AppConfigTopicMaintenanceContextWindow: " 262144 "})
	require.NoError(t, err)
	require.Equal(t, "262144", values[domain.AppConfigTopicMaintenanceContextWindow])
}
