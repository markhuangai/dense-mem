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
	assert.Equal(t, "", appConfigItem(settings, domain.AppConfigSSOPublicBaseURL).Value)
	assert.Equal(t, "300", appConfigItem(settings, domain.AppConfigSSOEntitlementCacheTTLSeconds).EffectiveValue)
	assert.Equal(t, "28800", appConfigItem(settings, domain.AppConfigSSOSessionTTLSeconds).EffectiveValue)
	assert.Equal(t, "600", appConfigItem(settings, domain.AppConfigSSOStateTTLSeconds).EffectiveValue)
	assert.Equal(t, "10", appConfigItem(settings, domain.AppConfigSSOHTTPTimeoutSeconds).EffectiveValue)
	assert.Equal(t, "false", appConfigItem(settings, domain.AppConfigSSOCookieSecure).EffectiveValue)

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
	assert.Equal(t, "https://portal.example.com", appConfigItem(updated, domain.AppConfigSSOPublicBaseURL).Value)
	assert.Equal(t, "3600", appConfigItem(updated, domain.AppConfigSSOSessionTTLSeconds).EffectiveValue)
	assert.Equal(t, "true", appConfigItem(updated, domain.AppConfigSSOCookieSecure).EffectiveValue)
	assert.Equal(t, now.Format(time.RFC3339Nano), updated.UpdateTime)

	runtime, err = svc.SSORuntimeConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://portal.example.com", runtime.PublicBaseURL)
	assert.Equal(t, time.Hour, runtime.SessionTTL)
	assert.True(t, runtime.CookieSecure)
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
	assert.Equal(t, "UTC", dreamingConfigItemForTest(settings, domain.AppConfigDreamingTimezone).EffectiveValue)
	assert.Equal(t, "true", dreamingConfigItemForTest(settings, domain.AppConfigDreamingReflectEnabled).EffectiveValue)
	assert.Equal(t, "true", dreamingConfigItemForTest(settings, domain.AppConfigDreamingReevaluateEnabled).EffectiveValue)
	assert.Equal(t, "true", dreamingConfigItemForTest(settings, domain.AppConfigDreamingDreamEnabled).EffectiveValue)
	assert.Equal(t, "5", dreamingConfigItemForTest(settings, domain.AppConfigDreamingMaxOutputs).EffectiveValue)

	runtime, err := svc.DreamingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.False(t, runtime.Enabled)
	assert.False(t, runtime.ForceEnabled)
	assert.True(t, runtime.ReflectEnabled)
	assert.True(t, runtime.ReevaluateEnabled)
	assert.True(t, runtime.DreamEnabled)
	assert.Equal(t, "03:00", runtime.StartTimeLocal)
	assert.Equal(t, "UTC", runtime.Timezone)
	assert.Equal(t, 5, runtime.MaxOutputs)

	now = now.Add(time.Minute)
	updated, err := svc.UpdateDreamingSettings(ctx, map[string]string{
		domain.AppConfigDreamingEnabled:        "true",
		domain.AppConfigDreamingStartTimeLocal: "02:30",
		domain.AppConfigDreamingTimezone:       "America/New_York",
		domain.AppConfigDreamingDreamEnabled:   "false",
		domain.AppConfigDreamingMaxOutputs:     "9",
	}, "control", "127.0.0.1", "corr")
	require.NoError(t, err)
	assert.Equal(t, "true", dreamingConfigItemForTest(updated, domain.AppConfigDreamingEnabled).EffectiveValue)
	assert.Equal(t, "02:30", dreamingConfigItemForTest(updated, domain.AppConfigDreamingStartTimeLocal).EffectiveValue)
	assert.Equal(t, "America/New_York", dreamingConfigItemForTest(updated, domain.AppConfigDreamingTimezone).EffectiveValue)
	assert.Equal(t, "false", dreamingConfigItemForTest(updated, domain.AppConfigDreamingDreamEnabled).EffectiveValue)
	assert.Equal(t, "9", dreamingConfigItemForTest(updated, domain.AppConfigDreamingMaxOutputs).EffectiveValue)

	runtime, err = svc.DreamingRuntimeConfig(ctx)
	require.NoError(t, err)
	assert.True(t, runtime.Enabled)
	assert.Equal(t, "02:30", runtime.StartTimeLocal)
	assert.Equal(t, "America/New_York", runtime.Timezone)
	assert.False(t, runtime.DreamEnabled)
	assert.Equal(t, 9, runtime.MaxOutputs)
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
			assert.Equal(t, tt.wantEffective, appConfigItem(settings, domain.AppConfigSSOCookieSecure).EffectiveValue)

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

	repo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: "v2", UpdatedAt: now}
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

	repo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: "v2", UpdatedAt: now}
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

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigUpdateTimeKey: "v2"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigSSOSessionTTLSeconds: "0"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigSSOCookieSecure: "maybe"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateSSOSettings(ctx, map[string]string{domain.AppConfigSSOPublicBaseURL: "://bad"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{"unknown": "value"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigUpdateTimeKey: "v2"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingEnabled: "maybe"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingStartTimeLocal: "25:99"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingTimezone: "Nope/Zone"}, "control", "", "")
	require.ErrorIs(t, err, ErrInvalidAppConfig)

	_, err = svc.UpdateDreamingSettings(ctx, map[string]string{domain.AppConfigDreamingMaxOutputs: "0"}, "control", "", "")
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
	assert.Equal(t, "https://portal.example.com", appConfigItem(updated, domain.AppConfigSSOPublicBaseURL).Value)
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
	assert.Nil(t, ssoSettingsPayload(nil))
	assert.Nil(t, dreamingSettingsPayload(nil))
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
	refreshRepo.entries[domain.AppConfigUpdateTimeKey] = domain.AppConfigEntry{Key: domain.AppConfigUpdateTimeKey, Value: "v2", UpdatedAt: now}
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
	for _, key := range editableSSOConfigKeys() {
		entries[key] = domain.AppConfigEntry{Key: key, Value: "", UpdatedAt: now}
	}
	for _, key := range editableDreamingConfigKeys() {
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

func appConfigItem(settings *domain.SSOConfigSettings, key string) domain.SSOConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.SSOConfigItem{}
}

func dreamingConfigItemForTest(settings *domain.DreamingConfigSettings, key string) domain.DreamingConfigItem {
	for _, item := range settings.Items {
		if item.Key == key {
			return item
		}
	}
	return domain.DreamingConfigItem{}
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
