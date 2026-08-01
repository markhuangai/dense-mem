package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const DefaultAppConfigCacheCheckInterval = 5 * time.Second
const DefaultAppTimezone = "Local"
const DefaultOperationLogRetentionDays = 30
const DefaultRecallFeedbackRetentionDays = 30
const DefaultEvaluationExportMaxPageSize = 100
const DefaultCommunityDetectionStartTimeLocal = "03:30"
const DefaultCommunityDetectionMaxConcurrency = 1
const DefaultCommunityDetectionJitterSeconds = 600

var ErrInvalidAppConfig = errors.New("invalid app config")

type AppConfigService interface {
	GetGeneralSettings(ctx context.Context) (*domain.GeneralConfigSettings, error)
	UpdateGeneralSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.GeneralConfigSettings, error)
	GeneralRuntimeConfig(ctx context.Context) (domain.GeneralRuntimeConfig, error)
	GetSSOSettings(ctx context.Context) (*domain.SSOConfigSettings, error)
	UpdateSSOSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.SSOConfigSettings, error)
	SSORuntimeConfig(ctx context.Context) (SSORuntimeConfig, error)
	GetDreamingSettings(ctx context.Context) (*domain.DreamingConfigSettings, error)
	UpdateDreamingSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.DreamingConfigSettings, error)
	DreamingRuntimeConfig(ctx context.Context) (domain.DreamingRuntimeConfig, error)
	GetCommunityDetectionSettings(ctx context.Context) (*domain.CommunityDetectionConfigSettings, error)
	UpdateCommunityDetectionSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.CommunityDetectionConfigSettings, error)
	CommunityDetectionRuntimeConfig(ctx context.Context) (domain.CommunityDetectionRuntimeConfig, error)
	GetOperationLogSettings(ctx context.Context) (*domain.OperationLogConfigSettings, error)
	UpdateOperationLogSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.OperationLogConfigSettings, error)
	OperationLogRuntimeConfig(ctx context.Context) (domain.OperationLogRuntimeConfig, error)
	GetRecallFeedbackSettings(ctx context.Context) (*domain.RecallFeedbackConfigSettings, error)
	UpdateRecallFeedbackSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.RecallFeedbackConfigSettings, error)
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
	GetEvaluationSettings(ctx context.Context) (*domain.EvaluationConfigSettings, error)
	UpdateEvaluationSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.EvaluationConfigSettings, error)
	EvaluationRuntimeConfig(ctx context.Context) (domain.EvaluationRuntimeConfig, error)
	GetTelemetryPricingSettings(ctx context.Context) (*domain.TelemetryPricingConfigSettings, error)
	UpdateTelemetryPricingSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.TelemetryPricingConfigSettings, error)
	TelemetryPricingRuntimeConfig(ctx context.Context) (domain.TelemetryPricingRuntimeConfig, error)
}

type AppConfigServiceImpl struct {
	repo          repository.AppConfigRepository
	audit         AuditService
	now           func() time.Time
	checkInterval time.Duration

	mu    sync.Mutex
	cache *appConfigCache
}

type appConfigCache struct {
	updateTime string
	entries    map[string]domain.AppConfigEntry
	general    domain.GeneralConfigSettings
	sso        SSORuntimeConfig
	settings   domain.SSOConfigSettings
	dreaming   domain.DreamingConfigSettings
	community  domain.CommunityDetectionConfigSettings
	opLogs     domain.OperationLogConfigSettings
	recall     domain.RecallFeedbackConfigSettings
	evaluation domain.EvaluationConfigSettings
	telemetry  domain.TelemetryPricingConfigSettings
	checkedAt  time.Time
}

var _ AppConfigService = (*AppConfigServiceImpl)(nil)

func NewAppConfigService(repo repository.AppConfigRepository, audit AuditService) *AppConfigServiceImpl {
	return &AppConfigServiceImpl{
		repo:          repo,
		audit:         audit,
		now:           func() time.Time { return time.Now().UTC() },
		checkInterval: DefaultAppConfigCacheCheckInterval,
	}
}

func (s *AppConfigServiceImpl) GetGeneralSettings(ctx context.Context) (*domain.GeneralConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.general
	settings.Items = append([]domain.GeneralConfigItem(nil), cache.general.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateGeneralSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.GeneralConfigSettings, error) {
	normalized, err := normalizeGeneralConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetGeneralSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetGeneralSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "general", actorRole, clientIP, correlationID, generalSettingsPayload(before), generalSettingsPayload(updated), map[string]any{"section": "general"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) GeneralRuntimeConfig(ctx context.Context) (domain.GeneralRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.GeneralRuntimeConfig{}, err
	}
	return cache.general.Effective, nil
}

func (s *AppConfigServiceImpl) GetSSOSettings(ctx context.Context) (*domain.SSOConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.settings
	settings.Items = append([]domain.SSOConfigItem(nil), cache.settings.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateSSOSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.SSOConfigSettings, error) {
	normalized, err := normalizeSSOConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetSSOSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetSSOSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "sso", actorRole, clientIP, correlationID, ssoSettingsPayload(before), ssoSettingsPayload(updated), map[string]any{"section": "sso"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) SSORuntimeConfig(ctx context.Context) (SSORuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return SSORuntimeConfig{}, err
	}
	return cache.sso, nil
}

func (s *AppConfigServiceImpl) GetDreamingSettings(ctx context.Context) (*domain.DreamingConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.dreaming
	settings.Items = append([]domain.DreamingConfigItem(nil), cache.dreaming.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateDreamingSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.DreamingConfigSettings, error) {
	normalized, err := normalizeDreamingConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetDreamingSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetDreamingSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "dreaming", actorRole, clientIP, correlationID, dreamingSettingsPayload(before), dreamingSettingsPayload(updated), map[string]any{"section": "dreaming"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) DreamingRuntimeConfig(ctx context.Context) (domain.DreamingRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.DreamingRuntimeConfig{}, err
	}
	return cache.dreaming.Effective, nil
}

func (s *AppConfigServiceImpl) GetCommunityDetectionSettings(ctx context.Context) (*domain.CommunityDetectionConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.community
	settings.Items = append([]domain.CommunityDetectionConfigItem(nil), cache.community.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateCommunityDetectionSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.CommunityDetectionConfigSettings, error) {
	normalized, err := normalizeCommunityDetectionConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetCommunityDetectionSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetCommunityDetectionSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "community_detection", actorRole, clientIP, correlationID, communityDetectionSettingsPayload(before), communityDetectionSettingsPayload(updated), map[string]any{"section": "community_detection"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) CommunityDetectionRuntimeConfig(ctx context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.CommunityDetectionRuntimeConfig{}, err
	}
	return cache.community.Effective, nil
}

func (s *AppConfigServiceImpl) GetOperationLogSettings(ctx context.Context) (*domain.OperationLogConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.opLogs
	settings.Items = append([]domain.OperationLogConfigItem(nil), cache.opLogs.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateOperationLogSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.OperationLogConfigSettings, error) {
	normalized, err := normalizeOperationLogConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetOperationLogSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetOperationLogSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "operation_logs", actorRole, clientIP, correlationID, operationLogSettingsPayload(before), operationLogSettingsPayload(updated), map[string]any{"section": "operation_logs"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) OperationLogRuntimeConfig(ctx context.Context) (domain.OperationLogRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.OperationLogRuntimeConfig{}, err
	}
	return cache.opLogs.Effective, nil
}

func (s *AppConfigServiceImpl) currentCache(ctx context.Context) (*appConfigCache, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("app config service is unavailable")
	}
	now := s.now().UTC()

	s.mu.Lock()
	if s.cache != nil && now.Sub(s.cache.checkedAt) < s.cacheInterval() {
		cache := cloneAppConfigCache(s.cache)
		s.mu.Unlock()
		return cache, nil
	}
	s.mu.Unlock()

	updateTime, err := s.repo.GetUpdateTime(ctx)
	if err != nil {
		return s.cachedOrError(now, err)
	}
	s.mu.Lock()
	if s.cache != nil && updateTime == s.cache.updateTime {
		s.cache.checkedAt = now
		cache := cloneAppConfigCache(s.cache)
		s.mu.Unlock()
		return cache, nil
	}
	s.mu.Unlock()

	entries, err := s.repo.List(ctx)
	if err != nil {
		return s.cachedOrError(now, err)
	}
	next, err := buildAppConfigCache(entries, now)
	if err != nil {
		return s.cachedOrError(now, err)
	}
	s.mu.Lock()
	s.cache = next
	cache := cloneAppConfigCache(s.cache)
	s.mu.Unlock()
	return cache, nil
}

func (s *AppConfigServiceImpl) cachedOrError(checkedAt time.Time, err error) (*appConfigCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		s.cache.checkedAt = checkedAt
		return cloneAppConfigCache(s.cache), nil
	}
	return nil, err
}

func (s *AppConfigServiceImpl) cacheInterval() time.Duration {
	if s.checkInterval <= 0 {
		return DefaultAppConfigCacheCheckInterval
	}
	return s.checkInterval
}

func (s *AppConfigServiceImpl) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
}

func buildAppConfigCache(entries map[string]domain.AppConfigEntry, checkedAt time.Time) (*appConfigCache, error) {
	updateEntry, ok := entries[domain.AppConfigUpdateTimeKey]
	if !ok || strings.TrimSpace(updateEntry.Value) == "" {
		return nil, fmt.Errorf("%w: update_time is required", ErrInvalidAppConfig)
	}
	general, err := generalRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	runtime, settings, err := ssoRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	dreaming, err := dreamingRuntimeConfigFromEntries(entries, general.Effective.Timezone)
	if err != nil {
		return nil, err
	}
	community, err := communityDetectionRuntimeConfigFromEntries(entries, general.Effective.Timezone)
	if err != nil {
		return nil, err
	}
	opLogs, err := operationLogRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	recall, err := recallFeedbackRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	evaluation, err := evaluationRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	telemetry, err := telemetryPricingRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	return &appConfigCache{
		updateTime: updateEntry.Value,
		entries:    cloneAppConfigEntries(entries),
		general:    general,
		sso:        runtime,
		settings:   settings,
		dreaming:   dreaming,
		community:  community,
		opLogs:     opLogs,
		recall:     recall,
		evaluation: evaluation,
		telemetry:  telemetry,
		checkedAt:  checkedAt,
	}, nil
}

func generalRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.GeneralConfigSettings, error) {
	values := make(map[string]string, len(editableGeneralConfigKeys()))
	for _, key := range editableGeneralConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizeGeneralConfigValues(values)
	if err != nil {
		return domain.GeneralConfigSettings{}, err
	}

	timezone := configString(normalized[domain.AppConfigTimezone], DefaultAppTimezone)
	runtime := domain.GeneralRuntimeConfig{Timezone: timezone}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.GeneralConfigItem{
		generalConfigItem(entries, domain.AppConfigTimezone, timezone),
	}
	return domain.GeneralConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func ssoRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (SSORuntimeConfig, domain.SSOConfigSettings, error) {
	values := make(map[string]string, len(editableSSOConfigKeys()))
	for _, key := range editableSSOConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}

	normalized, err := normalizeSSOConfigValues(values)
	if err != nil {
		return SSORuntimeConfig{}, domain.SSOConfigSettings{}, err
	}

	entitlementTTL, entitlementEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOEntitlementCacheTTLSeconds], DefaultSSOEntitlementCacheTTL)
	sessionTTL, sessionEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOSessionTTLSeconds], DefaultSSOSessionTTL)
	stateTTL, stateEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOStateTTLSeconds], DefaultSSOStateTTL)
	httpTimeout, httpEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOHTTPTimeoutSeconds], DefaultSSOHTTPTimeout)
	cookieSecureDefault := defaultSSOCookieSecure(normalized[domain.AppConfigSSOPublicBaseURL])
	cookieSecure, cookieEffective := ssoConfigBool(normalized[domain.AppConfigSSOCookieSecure], cookieSecureDefault)

	runtime := SSORuntimeConfig{
		PublicBaseURL:        normalized[domain.AppConfigSSOPublicBaseURL],
		SCIMPublicBaseURL:    normalized[domain.AppConfigSCIMPublicBaseURL],
		ControlPublicBaseURL: normalized[domain.AppConfigControlPublicBaseURL],
		EntitlementCacheTTL:  entitlementTTL,
		SessionTTL:           sessionTTL,
		StateTTL:             stateTTL,
		CookieSecure:         cookieSecure,
		HTTPTimeout:          httpTimeout,
	}

	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.SSOConfigItem{
		ssoConfigItem(entries, domain.AppConfigSSOPublicBaseURL, normalized[domain.AppConfigSSOPublicBaseURL]),
		ssoConfigItem(entries, domain.AppConfigSCIMPublicBaseURL, normalized[domain.AppConfigSCIMPublicBaseURL]),
		ssoConfigItem(entries, domain.AppConfigControlPublicBaseURL, normalized[domain.AppConfigControlPublicBaseURL]),
		ssoConfigItem(entries, domain.AppConfigSSOEntitlementCacheTTLSeconds, entitlementEffective),
		ssoConfigItem(entries, domain.AppConfigSSOSessionTTLSeconds, sessionEffective),
		ssoConfigItem(entries, domain.AppConfigSSOStateTTLSeconds, stateEffective),
		ssoConfigItem(entries, domain.AppConfigSSOHTTPTimeoutSeconds, httpEffective),
		ssoConfigItem(entries, domain.AppConfigSSOCookieSecure, cookieEffective),
	}

	return runtime, domain.SSOConfigSettings{UpdateTime: updateTime, Items: items}, nil
}

func dreamingRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry, timezone string) (domain.DreamingConfigSettings, error) {
	values := make(map[string]string, len(editableDreamingConfigKeys()))
	for _, key := range editableDreamingConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizeDreamingConfigValues(values)
	if err != nil {
		return domain.DreamingConfigSettings{}, err
	}

	enabled, enabledEffective := dreamingConfigBool(normalized[domain.AppConfigDreamingEnabled], false)
	forceEnabled, forceEffective := dreamingConfigBool(normalized[domain.AppConfigDreamingForceEnabled], false)
	maxOutputs, maxOutputsEffective := dreamingConfigInt(normalized[domain.AppConfigDreamingMaxOutputs], 5)
	startTime := configString(normalized[domain.AppConfigDreamingStartTimeLocal], "03:00")

	runtime := domain.DreamingRuntimeConfig{
		Enabled:        enabled,
		ForceEnabled:   forceEnabled,
		StartTimeLocal: startTime,
		Timezone:       timezone,
		MaxOutputs:     maxOutputs,
	}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.DreamingConfigItem{
		dreamingConfigItem(entries, domain.AppConfigDreamingEnabled, enabledEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingForceEnabled, forceEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingStartTimeLocal, startTime),
		dreamingConfigItem(entries, domain.AppConfigDreamingMaxOutputs, maxOutputsEffective),
	}
	return domain.DreamingConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func normalizeDreamingConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableDreamingConfigKeys()))
	for _, key := range editableDreamingConfigKeys() {
		allowed[key] = struct{}{}
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		if key == domain.AppConfigUpdateTimeKey {
			return nil, fmt.Errorf("%w: update_time is read-only", ErrInvalidAppConfig)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: unknown key %s", ErrInvalidAppConfig, key)
		}
		trimmed := strings.TrimSpace(value)
		switch key {
		case domain.AppConfigDreamingEnabled, domain.AppConfigDreamingForceEnabled:
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return nil, fmt.Errorf("%w: %s must be true or false", ErrInvalidAppConfig, key)
				}
				trimmed = strconv.FormatBool(parsed)
			}
		case domain.AppConfigDreamingStartTimeLocal:
			if trimmed == "" {
				trimmed = "03:00"
			}
			if _, err := time.Parse("15:04", trimmed); err != nil {
				return nil, fmt.Errorf("%w: DREAMING_START_TIME_LOCAL must use HH:MM 24-hour format", ErrInvalidAppConfig)
			}
		case domain.AppConfigDreamingMaxOutputs:
			if trimmed == "" {
				trimmed = "5"
			}
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed <= 0 || parsed > 50 {
				return nil, fmt.Errorf("%w: DREAMING_MAX_OUTPUTS must be between 1 and 50", ErrInvalidAppConfig)
			}
			trimmed = strconv.Itoa(parsed)
		}
		normalized[key] = trimmed
	}
	return normalized, nil
}

func communityDetectionRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry, timezone string) (domain.CommunityDetectionConfigSettings, error) {
	values := make(map[string]string, len(editableCommunityDetectionConfigKeys()))
	for _, key := range editableCommunityDetectionConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizeCommunityDetectionConfigValues(values)
	if err != nil {
		return domain.CommunityDetectionConfigSettings{}, err
	}

	enabled, enabledEffective := communityDetectionConfigBool(normalized[domain.AppConfigCommunityDetectionEnabled], false)
	startTime := configString(normalized[domain.AppConfigCommunityDetectionStartTimeLocal], DefaultCommunityDetectionStartTimeLocal)
	maxConcurrency, maxConcurrencyEffective := communityDetectionConfigInt(normalized[domain.AppConfigCommunityDetectionMaxConcurrency], DefaultCommunityDetectionMaxConcurrency)
	jitterSeconds, jitterSecondsEffective := communityDetectionConfigInt(normalized[domain.AppConfigCommunityDetectionJitterSeconds], DefaultCommunityDetectionJitterSeconds)

	runtime := domain.CommunityDetectionRuntimeConfig{
		Enabled:        enabled,
		StartTimeLocal: startTime,
		Timezone:       timezone,
		MaxConcurrency: maxConcurrency,
		JitterSeconds:  jitterSeconds,
	}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.CommunityDetectionConfigItem{
		communityDetectionConfigItem(entries, domain.AppConfigCommunityDetectionEnabled, enabledEffective),
		communityDetectionConfigItem(entries, domain.AppConfigCommunityDetectionStartTimeLocal, startTime),
		communityDetectionConfigItem(entries, domain.AppConfigCommunityDetectionMaxConcurrency, maxConcurrencyEffective),
		communityDetectionConfigItem(entries, domain.AppConfigCommunityDetectionJitterSeconds, jitterSecondsEffective),
	}
	return domain.CommunityDetectionConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func normalizeCommunityDetectionConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableCommunityDetectionConfigKeys()))
	for _, key := range editableCommunityDetectionConfigKeys() {
		allowed[key] = struct{}{}
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		if key == domain.AppConfigUpdateTimeKey {
			return nil, fmt.Errorf("%w: update_time is read-only", ErrInvalidAppConfig)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: unknown key %s", ErrInvalidAppConfig, key)
		}
		trimmed := strings.TrimSpace(value)
		switch key {
		case domain.AppConfigCommunityDetectionEnabled:
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return nil, fmt.Errorf("%w: COMMUNITY_DETECTION_ENABLED must be true or false", ErrInvalidAppConfig)
				}
				trimmed = strconv.FormatBool(parsed)
			}
		case domain.AppConfigCommunityDetectionStartTimeLocal:
			if trimmed == "" {
				trimmed = DefaultCommunityDetectionStartTimeLocal
			}
			if _, err := time.Parse("15:04", trimmed); err != nil {
				return nil, fmt.Errorf("%w: COMMUNITY_DETECTION_START_TIME_LOCAL must use HH:MM 24-hour format", ErrInvalidAppConfig)
			}
		case domain.AppConfigCommunityDetectionMaxConcurrency:
			if trimmed == "" {
				trimmed = strconv.Itoa(DefaultCommunityDetectionMaxConcurrency)
			}
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed < 1 || parsed > 8 {
				return nil, fmt.Errorf("%w: COMMUNITY_DETECTION_MAX_CONCURRENCY must be between 1 and 8", ErrInvalidAppConfig)
			}
			trimmed = strconv.Itoa(parsed)
		case domain.AppConfigCommunityDetectionJitterSeconds:
			if trimmed == "" {
				trimmed = strconv.Itoa(DefaultCommunityDetectionJitterSeconds)
			}
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed < 0 || parsed > 3600 {
				return nil, fmt.Errorf("%w: COMMUNITY_DETECTION_JITTER_SECONDS must be between 0 and 3600", ErrInvalidAppConfig)
			}
			trimmed = strconv.Itoa(parsed)
		}
		normalized[key] = trimmed
	}
	return normalized, nil
}

func operationLogRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.OperationLogConfigSettings, error) {
	values := make(map[string]string, len(editableOperationLogConfigKeys()))
	for _, key := range editableOperationLogConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizeOperationLogConfigValues(values)
	if err != nil {
		return domain.OperationLogConfigSettings{}, err
	}
	retentionDays, retentionEffective := operationLogConfigInt(normalized[domain.AppConfigOperationLogRetentionDays], DefaultOperationLogRetentionDays)
	runtime := domain.OperationLogRuntimeConfig{RetentionDays: retentionDays}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.OperationLogConfigItem{
		operationLogConfigItem(entries, domain.AppConfigOperationLogRetentionDays, retentionEffective),
	}
	return domain.OperationLogConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func normalizeOperationLogConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableOperationLogConfigKeys()))
	for _, key := range editableOperationLogConfigKeys() {
		allowed[key] = struct{}{}
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		if key == domain.AppConfigUpdateTimeKey {
			return nil, fmt.Errorf("%w: update_time is read-only", ErrInvalidAppConfig)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: unknown key %s", ErrInvalidAppConfig, key)
		}
		trimmed := strings.TrimSpace(value)
		switch key {
		case domain.AppConfigOperationLogRetentionDays:
			if trimmed == "" {
				trimmed = strconv.Itoa(DefaultOperationLogRetentionDays)
			}
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed < 1 || parsed > 365 {
				return nil, fmt.Errorf("%w: OPERATION_LOG_RETENTION_DAYS must be between 1 and 365", ErrInvalidAppConfig)
			}
			trimmed = strconv.Itoa(parsed)
		}
		normalized[key] = trimmed
	}
	return normalized, nil
}

func normalizeGeneralConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableGeneralConfigKeys()))
	for _, key := range editableGeneralConfigKeys() {
		allowed[key] = struct{}{}
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		if key == domain.AppConfigUpdateTimeKey {
			return nil, fmt.Errorf("%w: update_time is read-only", ErrInvalidAppConfig)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: unknown key %s", ErrInvalidAppConfig, key)
		}
		trimmed := strings.TrimSpace(value)
		switch key {
		case domain.AppConfigTimezone:
			if trimmed == "" {
				trimmed = DefaultAppTimezone
			}
			if _, err := time.LoadLocation(trimmed); err != nil {
				return nil, fmt.Errorf("%w: APP_TIMEZONE must be a valid IANA timezone or Local", ErrInvalidAppConfig)
			}
		}
		normalized[key] = trimmed
	}
	return normalized, nil
}

func normalizeSSOConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableSSOConfigKeys()))
	for _, key := range editableSSOConfigKeys() {
		allowed[key] = struct{}{}
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		if key == domain.AppConfigUpdateTimeKey {
			return nil, fmt.Errorf("%w: update_time is read-only", ErrInvalidAppConfig)
		}
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%w: unknown key %s", ErrInvalidAppConfig, key)
		}
		trimmed := strings.TrimSpace(value)
		switch key {
		case domain.AppConfigSSOPublicBaseURL, domain.AppConfigSCIMPublicBaseURL, domain.AppConfigControlPublicBaseURL:
			normalized[key] = strings.TrimRight(trimmed, "/")
			if normalized[key] != "" {
				parsed, err := url.Parse(normalized[key])
				if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
					return nil, fmt.Errorf("%w: %s must be an absolute http or https URL", ErrInvalidAppConfig, key)
				}
				if (key == domain.AppConfigSCIMPublicBaseURL || key == domain.AppConfigControlPublicBaseURL) && parsed.Scheme != "https" {
					return nil, fmt.Errorf("%w: %s must use https", ErrInvalidAppConfig, key)
				}
				if parsed.User != nil {
					return nil, fmt.Errorf("%w: %s must not include credentials", ErrInvalidAppConfig, key)
				}
				if parsed.RawQuery != "" || parsed.Fragment != "" {
					return nil, fmt.Errorf("%w: %s must not include query or fragment", ErrInvalidAppConfig, key)
				}
			}
		case domain.AppConfigSSOEntitlementCacheTTLSeconds, domain.AppConfigSSOSessionTTLSeconds, domain.AppConfigSSOStateTTLSeconds, domain.AppConfigSSOHTTPTimeoutSeconds:
			if trimmed != "" {
				parsed, err := strconv.Atoi(trimmed)
				if err != nil || parsed <= 0 {
					return nil, fmt.Errorf("%w: %s must be a positive integer", ErrInvalidAppConfig, key)
				}
				trimmed = strconv.Itoa(parsed)
			}
			normalized[key] = trimmed
		case domain.AppConfigSSOCookieSecure:
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return nil, fmt.Errorf("%w: SSO_COOKIE_SECURE must be true or false", ErrInvalidAppConfig)
				}
				trimmed = strconv.FormatBool(parsed)
			}
			normalized[key] = trimmed
		}
	}
	return normalized, nil
}

func editableGeneralConfigKeys() []string {
	return []string{
		domain.AppConfigTimezone,
	}
}

func editableSSOConfigKeys() []string {
	return []string{
		domain.AppConfigSSOPublicBaseURL,
		domain.AppConfigSCIMPublicBaseURL,
		domain.AppConfigControlPublicBaseURL,
		domain.AppConfigSSOEntitlementCacheTTLSeconds,
		domain.AppConfigSSOSessionTTLSeconds,
		domain.AppConfigSSOStateTTLSeconds,
		domain.AppConfigSSOHTTPTimeoutSeconds,
		domain.AppConfigSSOCookieSecure,
	}
}

func editableDreamingConfigKeys() []string {
	return []string{
		domain.AppConfigDreamingEnabled,
		domain.AppConfigDreamingForceEnabled,
		domain.AppConfigDreamingStartTimeLocal,
		domain.AppConfigDreamingMaxOutputs,
	}
}

func editableCommunityDetectionConfigKeys() []string {
	return []string{
		domain.AppConfigCommunityDetectionEnabled,
		domain.AppConfigCommunityDetectionStartTimeLocal,
		domain.AppConfigCommunityDetectionMaxConcurrency,
		domain.AppConfigCommunityDetectionJitterSeconds,
	}
}

func editableOperationLogConfigKeys() []string {
	return []string{
		domain.AppConfigOperationLogRetentionDays,
	}
}

func ssoConfigSeconds(value string, fallback time.Duration) (time.Duration, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(int(fallback / time.Second))
	}
	seconds, _ := strconv.Atoi(value)
	return time.Duration(seconds) * time.Second, strconv.Itoa(seconds)
}

func ssoConfigBool(value string, fallback bool) (bool, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.FormatBool(fallback)
	}
	parsed, _ := strconv.ParseBool(value)
	return parsed, strconv.FormatBool(parsed)
}

func dreamingConfigBool(value string, fallback bool) (bool, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.FormatBool(fallback)
	}
	parsed, _ := strconv.ParseBool(value)
	return parsed, strconv.FormatBool(parsed)
}

func dreamingConfigInt(value string, fallback int) (int, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(fallback)
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, strconv.Itoa(parsed)
}

func communityDetectionConfigBool(value string, fallback bool) (bool, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.FormatBool(fallback)
	}
	parsed, _ := strconv.ParseBool(value)
	return parsed, strconv.FormatBool(parsed)
}

func communityDetectionConfigInt(value string, fallback int) (int, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(fallback)
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, strconv.Itoa(parsed)
}

func operationLogConfigInt(value string, fallback int) (int, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(fallback)
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, strconv.Itoa(parsed)
}

func configString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func defaultSSOCookieSecure(publicBaseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && parsed.Host != ""
}

func generalConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.GeneralConfigItem {
	entry := entries[key]
	return domain.GeneralConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}

func dreamingConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.DreamingConfigItem {
	entry := entries[key]
	return domain.DreamingConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}

func communityDetectionConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.CommunityDetectionConfigItem {
	entry := entries[key]
	return domain.CommunityDetectionConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}

func operationLogConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.OperationLogConfigItem {
	entry := entries[key]
	return domain.OperationLogConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}

func ssoConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.SSOConfigItem {
	entry := entries[key]
	return domain.SSOConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}

func cloneAppConfigCache(cache *appConfigCache) *appConfigCache {
	if cache == nil {
		return nil
	}
	copy := *cache
	copy.entries = cloneAppConfigEntries(cache.entries)
	copy.general.Items = append([]domain.GeneralConfigItem(nil), cache.general.Items...)
	copy.settings.Items = append([]domain.SSOConfigItem(nil), cache.settings.Items...)
	copy.dreaming.Items = append([]domain.DreamingConfigItem(nil), cache.dreaming.Items...)
	copy.community.Items = append([]domain.CommunityDetectionConfigItem(nil), cache.community.Items...)
	copy.opLogs.Items = append([]domain.OperationLogConfigItem(nil), cache.opLogs.Items...)
	copy.recall.Items = append([]domain.RecallFeedbackConfigItem(nil), cache.recall.Items...)
	copy.evaluation.Items = append([]domain.EvaluationConfigItem(nil), cache.evaluation.Items...)
	copy.telemetry.Items = append([]domain.TelemetryPricingConfigItem(nil), cache.telemetry.Items...)
	copy.telemetry.Effective = cloneTelemetryPricingRuntimeConfig(cache.telemetry.Effective)
	return &copy
}

func cloneAppConfigEntries(entries map[string]domain.AppConfigEntry) map[string]domain.AppConfigEntry {
	copy := make(map[string]domain.AppConfigEntry, len(entries))
	for key, value := range entries {
		copy[key] = value
	}
	return copy
}

func (s *AppConfigServiceImpl) appendAudit(operation, entityType, entityID, actorRole, clientIP, correlationID string, before, after, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	if actorRole == "" {
		actorRole = "system"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.audit.Append(ctx, AuditLogEntry{
		Operation:     operation,
		EntityType:    entityType,
		EntityID:      entityID,
		BeforePayload: before,
		AfterPayload:  after,
		ActorRole:     actorRole,
		ClientIP:      clientIP,
		CorrelationID: correlationID,
		Metadata:      metadata,
	})
}
