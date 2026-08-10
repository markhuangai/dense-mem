package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAppConfigServiceSSOSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetSSOSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, now.Format(time.RFC3339Nano), settings.UpdateTime)
	assert.Equal(t, "", appConfigItem(t, settings, domain.AppConfigSSOPublicBaseURL).Value)
	assert.Equal(t, "300", appConfigItem(t, settings, domain.AppConfigSSOEntitlementCacheTTLSeconds).EffectiveValue)
	assert.Equal(t, "28800", appConfigItem(t, settings, domain.AppConfigSSOSessionTTLSeconds).EffectiveValue)
	assert.Equal(t, "600", appConfigItem(t, settings, domain.AppConfigSSOStateTTLSeconds).EffectiveValue)
	assert.Equal(t, "10", appConfigItem(t, settings, domain.AppConfigSSOHTTPTimeoutSeconds).EffectiveValue)
	assert.Equal(t, "false", appConfigItem(t, settings, domain.AppConfigSSOCookieSecure).EffectiveValue)

	runtime, err := svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, DefaultSSOEntitlementCacheTTL, runtime.EntitlementCacheTTL)
	assert.Equal(t, DefaultSSOSessionTTL, runtime.SessionTTL)
	assert.Equal(t, DefaultSSOStateTTL, runtime.StateTTL)
	assert.Equal(t, DefaultSSOHTTPTimeout, runtime.HTTPTimeout)
	assert.False(t, runtime.CookieSecure)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateSSOSettings(ctx, map[string]string{
		domain.AppConfigSSOPublicBaseURL:     "https://portal.example.com/",
		domain.AppConfigSSOSessionTTLSeconds: "3600",
		domain.AppConfigSSOCookieSecure:      "true",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "https://portal.example.com", appConfigItem(t, updated, domain.AppConfigSSOPublicBaseURL).Value)
	assert.Equal(t, "3600", appConfigItem(t, updated, domain.AppConfigSSOSessionTTLSeconds).EffectiveValue)
	assert.Equal(t, "true", appConfigItem(t, updated, domain.AppConfigSSOCookieSecure).EffectiveValue)
	assert.Equal(t, now.Format(time.RFC3339Nano), updated.UpdateTime)

	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://portal.example.com", runtime.PublicBaseURL)
	assert.Equal(t, time.Hour, runtime.SessionTTL)
	assert.True(t, runtime.CookieSecure)
}

func TestAppConfigServiceGeneralSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetGeneralSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, now.Format(time.RFC3339Nano), settings.UpdateTime)
	assert.Equal(t, "Local", generalConfigItemForTest(settings, domain.AppConfigTimezone).EffectiveValue)
	assert.Equal(t, DefaultEmbeddingReconciliationStartTimeLocal, generalConfigItemForTest(settings, domain.AppConfigEmbeddingReconciliationStartTimeLocal).EffectiveValue)

	runtime, err := svc.GeneralRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Local", runtime.Timezone)
	assert.Equal(t, DefaultEmbeddingReconciliationStartTimeLocal, runtime.EmbeddingReconciliationStartTimeLocal)

	dreaming, err := svc.DreamingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Local", dreaming.Timezone)

	community, err := svc.CommunityDetectionRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Local", community.Timezone)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateGeneralSettings(ctx, map[string]string{
		domain.AppConfigTimezone:                              "America/New_York",
		domain.AppConfigEmbeddingReconciliationStartTimeLocal: "23:59",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "America/New_York", generalConfigItemForTest(updated, domain.AppConfigTimezone).EffectiveValue)
	assert.Equal(t, "23:59", generalConfigItemForTest(updated, domain.AppConfigEmbeddingReconciliationStartTimeLocal).EffectiveValue)

	runtime, err = svc.GeneralRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "23:59", runtime.EmbeddingReconciliationStartTimeLocal)

	dreaming, err = svc.DreamingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "America/New_York", dreaming.Timezone)

	community, err = svc.CommunityDetectionRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "America/New_York", community.Timezone)
}

func TestAppConfigServiceReadsLegacySingleDigitSchedule(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey:                         now.Format(time.RFC3339Nano),
		domain.AppConfigTimezone:                              "UTC",
		domain.AppConfigEmbeddingReconciliationStartTimeLocal: "4:30",
	})
	svc := NewAppConfigService(repo, nil)

	settings, err := svc.GetGeneralSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "04:30", generalConfigItemForTest(settings, domain.AppConfigEmbeddingReconciliationStartTimeLocal).EffectiveValue)

	runtime, err := svc.GeneralRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "04:30", runtime.EmbeddingReconciliationStartTimeLocal)
}

func TestAppConfigServiceDreamingSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetDreamingSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, now.Format(time.RFC3339Nano), settings.UpdateTime)
	assert.Equal(t, "false", dreamingConfigItemForTest(settings, domain.AppConfigDreamingEnabled).EffectiveValue)
	assert.Equal(t, "false", dreamingConfigItemForTest(settings, domain.AppConfigDreamingForceEnabled).EffectiveValue)
	assert.Equal(t, "03:00", dreamingConfigItemForTest(settings, domain.AppConfigDreamingStartTimeLocal).EffectiveValue)
	assert.Equal(t, "5", dreamingConfigItemForTest(settings, domain.AppConfigDreamingMaxOutputs).EffectiveValue)
	assert.Len(t, settings.Items, 4)

	runtime, err := svc.DreamingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.False(t, runtime.Enabled)
	assert.False(t, runtime.ForceEnabled)
	assert.Equal(t, "03:00", runtime.StartTimeLocal)
	assert.Equal(t, "Local", runtime.Timezone)
	assert.Equal(t, 5, runtime.MaxOutputs)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateDreamingSettings(ctx, map[string]string{
		domain.AppConfigDreamingEnabled:        "true",
		domain.AppConfigDreamingStartTimeLocal: "02:30",
		domain.AppConfigDreamingMaxOutputs:     "9",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "true", dreamingConfigItemForTest(updated, domain.AppConfigDreamingEnabled).EffectiveValue)
	assert.Equal(t, "02:30", dreamingConfigItemForTest(updated, domain.AppConfigDreamingStartTimeLocal).EffectiveValue)
	assert.Equal(t, "9", dreamingConfigItemForTest(updated, domain.AppConfigDreamingMaxOutputs).EffectiveValue)

	runtime, err = svc.DreamingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.True(t, runtime.Enabled)
	assert.Equal(t, "02:30", runtime.StartTimeLocal)
	assert.Equal(t, "Local", runtime.Timezone)
	assert.Equal(t, 9, runtime.MaxOutputs)
}

func TestAppConfigServiceCommunityDetectionSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 15, 3, 30, 0, 0, time.Local)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetCommunityDetectionSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, now.Format(time.RFC3339Nano), settings.UpdateTime)
	assert.Equal(t, "false", communityDetectionConfigItemForTest(settings, domain.AppConfigCommunityDetectionEnabled).EffectiveValue)
	assert.Equal(t, "03:30", communityDetectionConfigItemForTest(settings, domain.AppConfigCommunityDetectionStartTimeLocal).EffectiveValue)
	assert.Equal(t, "1", communityDetectionConfigItemForTest(settings, domain.AppConfigCommunityDetectionMaxConcurrency).EffectiveValue)
	assert.Equal(t, "600", communityDetectionConfigItemForTest(settings, domain.AppConfigCommunityDetectionJitterSeconds).EffectiveValue)

	runtime, err := svc.CommunityDetectionRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.False(t, runtime.Enabled)
	assert.Equal(t, "03:30", runtime.StartTimeLocal)
	assert.Equal(t, "Local", runtime.Timezone)
	assert.Equal(t, 1, runtime.MaxConcurrency)
	assert.Equal(t, 600, runtime.JitterSeconds)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateCommunityDetectionSettings(ctx, map[string]string{
		domain.AppConfigCommunityDetectionEnabled:        "true",
		domain.AppConfigCommunityDetectionStartTimeLocal: "02:45",
		domain.AppConfigCommunityDetectionMaxConcurrency: "2",
		domain.AppConfigCommunityDetectionJitterSeconds:  "0",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "true", communityDetectionConfigItemForTest(updated, domain.AppConfigCommunityDetectionEnabled).EffectiveValue)
	assert.Equal(t, "02:45", communityDetectionConfigItemForTest(updated, domain.AppConfigCommunityDetectionStartTimeLocal).EffectiveValue)
	assert.Equal(t, "2", communityDetectionConfigItemForTest(updated, domain.AppConfigCommunityDetectionMaxConcurrency).EffectiveValue)
	assert.Equal(t, "0", communityDetectionConfigItemForTest(updated, domain.AppConfigCommunityDetectionJitterSeconds).EffectiveValue)

	runtime, err = svc.CommunityDetectionRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.True(t, runtime.Enabled)
	assert.Equal(t, "02:45", runtime.StartTimeLocal)
	assert.Equal(t, "Local", runtime.Timezone)
	assert.Equal(t, 2, runtime.MaxConcurrency)
	assert.Equal(t, 0, runtime.JitterSeconds)
}

func TestAppConfigServiceOperationLogSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetOperationLogSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "30", operationLogConfigItemForTest(settings, domain.AppConfigOperationLogRetentionDays).EffectiveValue)

	runtime, err := svc.OperationLogRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, DefaultOperationLogRetentionDays, runtime.RetentionDays)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateOperationLogSettings(ctx, map[string]string{
		domain.AppConfigOperationLogRetentionDays: "45",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "45", operationLogConfigItemForTest(updated, domain.AppConfigOperationLogRetentionDays).EffectiveValue)

	runtime, err = svc.OperationLogRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, 45, runtime.RetentionDays)
}

func TestAppConfigServiceRecallFeedbackSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetRecallFeedbackSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, "false", recallFeedbackConfigItemForTest(settings, domain.AppConfigRecallFeedbackEnabled).EffectiveValue)
	assert.Equal(t, "30", recallFeedbackConfigItemForTest(settings, domain.AppConfigRecallFeedbackRetentionDays).EffectiveValue)

	runtime, err := svc.RecallFeedbackRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.False(t, runtime.Enabled)
	assert.Equal(t, DefaultRecallFeedbackRetentionDays, runtime.RetentionDays)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateRecallFeedbackSettings(ctx, map[string]string{
		domain.AppConfigRecallFeedbackEnabled:       "true",
		domain.AppConfigRecallFeedbackRetentionDays: "45",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "true", recallFeedbackConfigItemForTest(updated, domain.AppConfigRecallFeedbackEnabled).EffectiveValue)
	assert.Equal(t, "45", recallFeedbackConfigItemForTest(updated, domain.AppConfigRecallFeedbackRetentionDays).EffectiveValue)

	runtime, err = svc.RecallFeedbackRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.True(t, runtime.Enabled)
	assert.Equal(t, 45, runtime.RetentionDays)
}

func TestAppConfigServiceTelemetryPricingSettingsDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey: now.Format(time.RFC3339Nano),
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	settings, err := svc.GetTelemetryPricingSettings(ctx)
	require.NoError(t, err)
	assert.Empty(t, telemetryPricingConfigItemForTest(settings, domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens).EffectiveValue)
	assert.Nil(t, settings.Effective.VerifierInputUSDPerMillionTokens)
	assert.Nil(t, settings.Effective.VerifierOutputUSDPerMillionTokens)
	assert.Nil(t, settings.Effective.EmbeddingInputUSDPerMillionTokens)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateTelemetryPricingSettings(ctx, map[string]string{
		domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens:  "1.25",
		domain.AppConfigTelemetryCostVerifierOutputUSDPerMillionTokens: "2.5",
		domain.AppConfigTelemetryCostEmbeddingInputUSDPerMillionTokens: "0.1",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "1.25", telemetryPricingConfigItemForTest(updated, domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens).EffectiveValue)
	assert.Equal(t, "2.5", telemetryPricingConfigItemForTest(updated, domain.AppConfigTelemetryCostVerifierOutputUSDPerMillionTokens).EffectiveValue)
	assert.Equal(t, "0.1", telemetryPricingConfigItemForTest(updated, domain.AppConfigTelemetryCostEmbeddingInputUSDPerMillionTokens).EffectiveValue)

	runtime, err := svc.TelemetryPricingRuntimeConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, runtime.VerifierInputUSDPerMillionTokens)
	require.NotNil(t, runtime.VerifierOutputUSDPerMillionTokens)
	require.NotNil(t, runtime.EmbeddingInputUSDPerMillionTokens)
	assert.Equal(t, 1.25, *runtime.VerifierInputUSDPerMillionTokens)
	assert.Equal(t, 2.5, *runtime.VerifierOutputUSDPerMillionTokens)
	assert.Equal(t, 0.1, *runtime.EmbeddingInputUSDPerMillionTokens)

	*runtime.VerifierInputUSDPerMillionTokens = 99
	runtime, err = svc.TelemetryPricingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1.25, *runtime.VerifierInputUSDPerMillionTokens)
}

func TestAppConfigServiceCachedTelemetryPricingRuntimeConfigDoesNotReadRepository(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey:                                 now.Format(time.RFC3339Nano),
		domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens: "1.25",
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	_, ok := svc.CachedTelemetryPricingRuntimeConfig()
	assert.False(t, ok)

	_, err := svc.TelemetryPricingRuntimeConfig(ctx)
	require.NoError(t, err)
	updateTimeCalls := repo.updateTimeCalls
	listCalls := repo.listCalls

	runtime, ok := svc.CachedTelemetryPricingRuntimeConfig()
	require.True(t, ok)
	require.NotNil(t, runtime.VerifierInputUSDPerMillionTokens)
	assert.Equal(t, 1.25, *runtime.VerifierInputUSDPerMillionTokens)
	assert.Equal(t, updateTimeCalls, repo.updateTimeCalls)
	assert.Equal(t, listCalls, repo.listCalls)
}

func TestAppConfigServiceTreatsInvalidStoredTelemetryPricingAsUnpriced(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey:                                 now.Format(time.RFC3339Nano),
		domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens: "not-a-number",
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	_, err := svc.GetGeneralSettings(ctx)
	require.NoError(t, err)

	settings, err := svc.GetTelemetryPricingSettings(ctx)
	require.NoError(t, err)
	item := telemetryPricingConfigItemForTest(settings, domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens)
	require.Equal(t, "not-a-number", item.Value)
	require.Empty(t, item.EffectiveValue)
	require.Equal(t, "TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS must be a number between 0 and 1000000", item.ValidationError)
	require.Nil(t, settings.Effective.VerifierInputUSDPerMillionTokens)
}

func TestAppConfigServiceSSOCookieSecureEffectiveDefault(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		publicBaseURL string
		cookieSecure  string
		wantSecure    bool
		wantEffective string
	}{
		{
			name:          "https public URL defaults secure cookies",
			publicBaseURL: "https://portal.example.com",
			wantSecure:    true,
			wantEffective: "true",
		},
		{
			name:          "http public URL defaults insecure cookies",
			publicBaseURL: "http://localhost:8080",
			wantSecure:    false,
			wantEffective: "false",
		},
		{
			name:          "explicit false overrides https public URL",
			publicBaseURL: "https://portal.example.com",
			cookieSecure:  "false",
			wantSecure:    false,
			wantEffective: "false",
		},
		{
			name:          "explicit true overrides http public URL",
			publicBaseURL: "http://localhost:8080",
			cookieSecure:  "true",
			wantSecure:    true,
			wantEffective: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newAppConfigRepoStub(now, map[string]string{
				domain.AppConfigUpdateTimeKey:    now.Format(time.RFC3339Nano),
				domain.AppConfigSSOPublicBaseURL: tt.publicBaseURL,
				domain.AppConfigSSOCookieSecure:  tt.cookieSecure,
			})
			svc := NewAppConfigService(repo, nil)
			svc.now = func() time.Time { return now }

			settings, err := svc.GetSSOSettings(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.wantEffective, appConfigItem(t, settings, domain.AppConfigSSOCookieSecure).EffectiveValue)

			runtime, err := svc.SSORuntimeConfig(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.publicBaseURL, runtime.PublicBaseURL)
			assert.Equal(t, tt.wantSecure, runtime.CookieSecure)
		})
	}
}

func TestAppConfigServiceCachesUntilUpdateTimeChanges(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey:        "v1",
		domain.AppConfigSSOSessionTTLSeconds: "3600",
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	runtime, err := svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, runtime.SessionTTL)
	assert.Equal(t, 1, repo.listCalls)
	assert.Equal(t, 1, repo.updateTimeCalls)

	repo.entries[domain.AppConfigSSOSessionTTLSeconds] = domain.AppConfigEntry{Key: domain.AppConfigSSOSessionTTLSeconds, Value: "7200", UpdatedAt: now}
	now = now.Add(time.Second)
	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, runtime.SessionTTL)
	assert.Equal(t, 1, repo.updateTimeCalls)
	assert.Equal(t, 1, repo.listCalls)

	now = now.Add(5 * time.Second)
	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, runtime.SessionTTL)
	assert.Equal(t, 2, repo.updateTimeCalls)
	assert.Equal(t, 1, repo.listCalls)

	repo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: "canonical", UpdatedAt: now}
	now = now.Add(5 * time.Second)
	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, runtime.SessionTTL)
	assert.Equal(t, 3, repo.updateTimeCalls)
	assert.Equal(t, 2, repo.listCalls)
}

func TestAppConfigServiceKeepsLastKnownGoodWhenReloadIsInvalid(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey:         "v1",
		domain.AppConfigSSOHTTPTimeoutSeconds: "15",
	})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	runtime, err := svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, runtime.HTTPTimeout)

	repo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: "canonical", UpdatedAt: now}
	repo.entries[domain.AppConfigSSOHTTPTimeoutSeconds] = domain.AppConfigEntry{Key: domain.AppConfigSSOHTTPTimeoutSeconds, Value: "bad", UpdatedAt: now}
	now = now.Add(5 * time.Second)

	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, runtime.HTTPTimeout)
}

func TestAppConfigServiceValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{domain.AppConfigUpdateTimeKey: "v1"})
	svc := NewAppConfigService(repo, nil)
	svc.now = func() time.Time { return now }

	_, err := svc.UpdateSSOSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateGeneralSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateGeneralSettings(ctx, map[string]string{domain.AppConfigUpdateTimeKey: "canonical"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateGeneralSettings(ctx, map[string]string{domain.AppConfigTimezone: "Nope/Zone"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)
	require.ErrorContains(t, err, "APP_TIMEZONE must be a valid IANA timezone or Local")

	updated, err := svc.UpdateGeneralSettings(ctx, map[string]string{domain.AppConfigEmbeddingReconciliationStartTimeLocal: "4:30"}, "control", "", "")
	require.NoError(t, err)
	assert.Equal(t, "04:30", generalConfigItemForTest(updated, domain.AppConfigEmbeddingReconciliationStartTimeLocal).EffectiveValue)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigUpdateTimeKey: "canonical"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigSSOSessionTTLSeconds: "0"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigSSOCookieSecure: "maybe"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigSSOPublicBaseURL: "://bad"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigUpdateTimeKey: "canonical"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingEnabled: "maybe"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingStartTimeLocal: "25:99"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{"FEATURE_TIMEZONE": "UTC"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingMaxOutputs: "0"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{domain.AppConfigUpdateTimeKey: "canonical"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{domain.AppConfigCommunityDetectionEnabled: "maybe"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{domain.AppConfigCommunityDetectionStartTimeLocal: "25:99"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{"FEATURE_TIMEZONE": "Local"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{domain.AppConfigCommunityDetectionMaxConcurrency: "0"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateCommunityDetectionSettings(ctx, map[string]string{domain.AppConfigCommunityDetectionJitterSeconds: "-1"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateOperationLogSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateOperationLogSettings(ctx, map[string]string{domain.AppConfigOperationLogRetentionDays: "0"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateRecallFeedbackSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateRecallFeedbackSettings(ctx, map[string]string{domain.AppConfigRecallFeedbackEnabled: "maybe"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateRecallFeedbackSettings(ctx, map[string]string{domain.AppConfigRecallFeedbackRetentionDays: "0"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateRecallFeedbackSettings(ctx, map[string]string{domain.AppConfigRecallFeedbackRetentionDays: "366"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	for _, value := range []string{"-1", "NaN", "Inf", "1000000.01"} {
		_, err = svc.UpdateTelemetryPricingSettings(ctx, map[string]string{domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens: value}, "control", "", "")
		require.ErrorIs(t, err, ErrInvalidAppConfig)
	}
	_, err = svc.UpdateTelemetryPricingSettings(ctx, map[string]string{"unknown": "1"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)
}

func TestAppConfigServiceAuditNoopAndUnavailableBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	repo := newAppConfigRepoStub(now, map[string]string{domain.AppConfigUpdateTimeKey: "v1"})
	audit := &appConfigAuditStub{}
	svc := NewAppConfigService(repo, audit)
	svc.now = func() time.Time { return now }

	updated, err := svc.UpdateSSOSettings(ctx, map[string]string{
		domain.AppConfigSSOPublicBaseURL: "https://portal.example.com",
	}, "", "203.0.113.10", "corr")
	require.NoError(t, err)
	assert.Equal(t, "https://portal.example.com", appConfigItem(t, updated, domain.AppConfigSSOPublicBaseURL).Value)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, "APP_CONFIG_UPDATE", audit.entries[0].Operation)
	assert.Equal(t, "system", audit.entries[0].ActorRole)
	assert.Equal(t, "203.0.113.10", audit.entries[0].ClientIP)
	assert.Equal(t, "corr", audit.entries[0].CorrelationID)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{
		domain.AppConfigSSOPublicBaseURL: "https://portal.example.com",
	}, "control", "", "")
	require.NoError(t, err)
	assert.Len(t, audit.entries, 1)

	repo.updateErr = errors.New("update failed")
	_, err = svc.UpdateSSOSettings(ctx, map[string]string{
		domain.AppConfigSSOPublicBaseURL: "https://other.example.com",
	}, "control", "", "")
	require.ErrorContains(t, err, "update failed")

	_, err = (*AppConfigServiceImpl)(nil).SSORuntimeConfig(ctx)
	require.ErrorContains(t, err, "unavailable")

	assert.Equal(t, DefaultAppConfigCacheCheckInterval, (&AppConfigServiceImpl{checkInterval: -time.Second}).cacheInterval())
	assert.Nil(t, cloneAppConfigCache(nil))
	assert.Nil(t, generalSettingsPayload(nil))
	assert.Nil(t, ssoSettingsPayload(nil))
	assert.Nil(t, dreamingSettingsPayload(nil))
	assert.Nil(t, communityDetectionSettingsPayload(nil))
	assert.Nil(t, operationLogSettingsPayload(nil))
	assert.Nil(t, recallFeedbackSettingsPayload(nil))
	assert.Nil(t, telemetryPricingSettingsPayload(nil))
}

func TestAppConfigServiceUnavailableMethods(t *testing.T) {
	ctx := context.Background()
	var svc *AppConfigServiceImpl

	_, err := svc.GetGeneralSettings(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.GeneralRuntimeConfig(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.GetDreamingSettings(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.DreamingRuntimeConfig(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.GetCommunityDetectionSettings(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.CommunityDetectionRuntimeConfig(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.GetOperationLogSettings(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.OperationLogRuntimeConfig(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.GetRecallFeedbackSettings(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.RecallFeedbackRuntimeConfig(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.GetTelemetryPricingSettings(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
	_, err = svc.TelemetryPricingRuntimeConfig(ctx)
	require.ErrorContains(t, err, "app config service is unavailable")
}

func TestAppConfigServiceInitialLoadAndRefreshErrors(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)

	missingUpdate := newAppConfigRepoStub(now, map[string]string{})
	delete(missingUpdate.entries, domain.AppConfigUpdateTimeKey)
	svc := NewAppConfigService(missingUpdate, nil)
	svc.now = func() time.Time { return now }
	_, err := svc.GetSSOSettings(ctx)
	require.Error(t, err)

	getUpdateErr := newAppConfigRepoStub(now, map[string]string{domain.AppConfigUpdateTimeKey: "v1"})
	getUpdateErr.getUpdateErr = errors.New("update time failed")
	svc = NewAppConfigService(getUpdateErr, nil)
	svc.now = func() time.Time { return now }
	_, err = svc.GetSSOSettings(ctx)
	require.ErrorContains(t, err, "update time failed")

	listErr := newAppConfigRepoStub(now, map[string]string{domain.AppConfigUpdateTimeKey: "v1"})
	listErr.listErr = errors.New("list failed")
	svc = NewAppConfigService(listErr, nil)
	svc.now = func() time.Time { return now }
	_, err = svc.GetSSOSettings(ctx)
	require.ErrorContains(t, err, "list failed")

	refreshRepo := newAppConfigRepoStub(now, map[string]string{
		domain.AppConfigUpdateTimeKey:        "v1",
		domain.AppConfigSSOSessionTTLSeconds: "3600",
	})
	svc = NewAppConfigService(refreshRepo, nil)
	svc.now = func() time.Time { return now }
	runtime, err := svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, runtime.SessionTTL)

	now = now.Add(6 * time.Second)
	refreshRepo.getUpdateErr = errors.New("temporary failure")
	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, runtime.SessionTTL)

	refreshRepo.getUpdateErr = nil
	refreshRepo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: "canonical", UpdatedAt: now}
	refreshRepo.listErr = errors.New("temporary list failure")
	now = now.Add(6 * time.Second)
	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, runtime.SessionTTL)
}

type appConfigRepoStub struct {
	entries         map[string]domain.AppConfigEntry
	updateTimeCalls int
	listCalls       int
	getUpdateErr    error
	listErr         error
	updateErr       error
}

func newAppConfigRepoStub(now time.Time, values map[string]string) *appConfigRepoStub {
	entries := make(map[string]domain.AppConfigEntry)
	for _, key := range editableGeneralConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableSSOConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableDreamingConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableCommunityDetectionConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableOperationLogConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableRecallFeedbackConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableTelemetryPricingConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: now.Format(time.RFC3339Nano), UpdatedAt: now}
	for key, value := range values {
		entries[key] = domain.AppConfigEntry{Key: key, Value: value, UpdatedAt: now}
	}
	return &appConfigRepoStub{entries: entries}
}

func (r *appConfigRepoStub) GetUpdateTime(context.Context) (string, error) {
	r.updateTimeCalls++
	if r.getUpdateErr != nil {
		return "", r.getUpdateErr
	}
	entry, ok := r.entries[domain.AppConfigUpdateTimeKey]
	if !ok {
		return "", errors.New("missing update_time")
	}
	return entry.Value, nil
}

func (r *appConfigRepoStub) List(context.Context) (map[string]domain.AppConfigEntry, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	copy := make(map[string]domain.AppConfigEntry, len(r.entries))
	for key, value := range r.entries {
		copy[key] = value
	}
	return copy, nil
}

func (r *appConfigRepoStub) UpdateValues(_ context.Context, values map[string]string, updateTime string, now time.Time) (bool, error) {
	if r.updateErr != nil {
		return false, r.updateErr
	}
	changed := false
	for key, value := range values {
		current := r.entries[key]
		if current.Value == value {
			continue
		}
		r.entries[key] = domain.AppConfigEntry{Key: key, Value: value, UpdatedAt: now}
		changed = true
	}
	if changed {
		r.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: updateTime, UpdatedAt: now}
	}
	return changed, nil
}

func appConfigItem(t *testing.T, settings *domain.SSOConfigSettings, key string) domain.SSOConfigItem {
	t.Helper()
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	require.Failf(t, "missing app config item", "key %s not found", key)
	return domain.SSOConfigItem{}
}

func generalConfigItemForTest(settings *domain.GeneralConfigSettings, key string) domain.GeneralConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.GeneralConfigItem{}
}

func dreamingConfigItemForTest(settings *domain.DreamingConfigSettings, key string) domain.DreamingConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.DreamingConfigItem{}
}

func communityDetectionConfigItemForTest(settings *domain.CommunityDetectionConfigSettings, key string) domain.CommunityDetectionConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.CommunityDetectionConfigItem{}
}

func operationLogConfigItemForTest(settings *domain.OperationLogConfigSettings, key string) domain.OperationLogConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.OperationLogConfigItem{}
}

func recallFeedbackConfigItemForTest(settings *domain.RecallFeedbackConfigSettings, key string) domain.RecallFeedbackConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.RecallFeedbackConfigItem{}
}

func telemetryPricingConfigItemForTest(settings *domain.TelemetryPricingConfigSettings, key string) domain.TelemetryPricingConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.TelemetryPricingConfigItem{}
}

type appConfigAuditStub struct {
	entries []AuditLogEntry
}

func (a *appConfigAuditStub) Append(_ context.Context, entry AuditLogEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

func (a *appConfigAuditStub) List(context.Context, string, int, int) ([]AuditLogEntry, int, error) {
	return nil, 0, nil
}

func (a *appConfigAuditStub) ProfileCreated(context.Context, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) ProfileUpdated(context.Context, string, map[string]interface{}, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) ProfileDeleteBlocked(context.Context, string, map[string]interface{}, *string, string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) ProfileDeleted(context.Context, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) APIKeyCreated(context.Context, *string, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) APIKeyRevoked(context.Context, *string, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) AuthFailure(context.Context, *string, string, string, map[string]interface{}, string, string) error {
	return nil
}

func (a *appConfigAuditStub) CrossProfileDenied(context.Context, string, string, string, map[string]interface{}, string, string) error {
	return nil
}

func (a *appConfigAuditStub) RateLimited(context.Context, *string, string, map[string]interface{}, string, string) error {
	return nil
}

func (a *appConfigAuditStub) SystemQuery(context.Context, string, map[string]interface{}, *string, string, string, string) error {
	return nil
}

func (a *appConfigAuditStub) InvariantViolation(context.Context, string, string, string, map[string]interface{}, string, string) error {
	return nil
}
