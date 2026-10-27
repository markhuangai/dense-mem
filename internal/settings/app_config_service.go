package settings

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
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

const DefaultAppConfigCacheCheckInterval = 5 * time.Second
const DefaultAppTimezone = "Local"
const DefaultOperationLogRetentionDays = 30
const DefaultRecallFeedbackRetentionDays = 30
const DefaultCommunityDetectionStartTimeLocal = "03:30"
const DefaultCommunityDetectionMaxConcurrency = 1
const DefaultCommunityDetectionJitterSeconds = 600

const (
	DefaultSSOEntitlementCacheTTL = accessservice.DefaultSSOEntitlementCacheTTL
	DefaultSSOSessionTTL          = accessservice.DefaultSSOSessionTTL
	DefaultSSOStateTTL            = accessservice.DefaultSSOStateTTL
	DefaultSSOHTTPTimeout         = accessservice.DefaultSSOHTTPTimeout
)

var ErrInvalidAppConfig = errors.New("invalid app config")

type AppConfigService interface {
	GetOntologyMaintenanceSettings(context.Context) (*domain.OntologyMaintenanceSettings, error)
	UpdateOntologyMaintenanceSettings(context.Context, map[string]string, string, string, string) (*domain.OntologyMaintenanceSettings, error)
	OntologyMaintenanceRuntimeConfig(context.Context) (domain.OntologyMaintenanceConfig, error)
	GetGeneralSettings(ctx context.Context) (*domain.GeneralConfigSettings, error)
	UpdateGeneralSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.GeneralConfigSettings, error)
	GeneralRuntimeConfig(ctx context.Context) (domain.GeneralRuntimeConfig, error)
	GetSSOSettings(ctx context.Context) (*domain.SSOConfigSettings, error)
	UpdateSSOSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.SSOConfigSettings, error)
	SSORuntimeConfig(ctx context.Context) (accessservice.SSORuntimeConfig, error)

	GetOperationLogSettings(ctx context.Context) (*domain.OperationLogConfigSettings, error)
	UpdateOperationLogSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.OperationLogConfigSettings, error)
	OperationLogRuntimeConfig(ctx context.Context) (domain.OperationLogRuntimeConfig, error)
	PrivateMemoryConfigService
	GetRecallFeedbackSettings(ctx context.Context) (*domain.RecallFeedbackConfigSettings, error)
	UpdateRecallFeedbackSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.RecallFeedbackConfigSettings, error)
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
	GetTelemetryPricingSettings(ctx context.Context) (*domain.TelemetryPricingConfigSettings, error)
	UpdateTelemetryPricingSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.TelemetryPricingConfigSettings, error)
	TelemetryPricingRuntimeConfig(ctx context.Context) (domain.TelemetryPricingRuntimeConfig, error)
}

type AppConfigServiceImpl struct {
	repo          settingscontract.AppConfigRepository
	audit         accessservice.AuditService
	now           func() time.Time
	checkInterval time.Duration

	mu              sync.Mutex
	cache           *appConfigCache
	refreshGate     chan struct{}
	decisionOptions DecisionSettingsOptions
}

type appConfigCache struct {
	updateTime string
	entries    map[string]domain.AppConfigEntry
	general    domain.GeneralConfigSettings
	sso        accessservice.SSORuntimeConfig
	settings   domain.SSOConfigSettings

	opLogs      domain.OperationLogConfigSettings
	private     domain.PrivateMemoryConfigSettings
	recall      domain.RecallFeedbackConfigSettings
	telemetry   domain.TelemetryPricingConfigSettings
	checkedAt   time.Time
	verifiedAt  time.Time
	refreshErr  error
	invalidated bool
}

var _ AppConfigService = (*AppConfigServiceImpl)(nil)

func NewAppConfigService(repo settingscontract.AppConfigRepository, audit accessservice.AuditService) *AppConfigServiceImpl {
	return &AppConfigServiceImpl{
		repo:          repo,
		audit:         audit,
		now:           func() time.Time { return time.Now().UTC() },
		checkInterval: DefaultAppConfigCacheCheckInterval,
		refreshGate:   make(chan struct{}, 1),
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

func (s *AppConfigServiceImpl) SSORuntimeConfig(ctx context.Context) (accessservice.SSORuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return accessservice.SSORuntimeConfig{}, err
	}
	return cache.sso, nil
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cache := s.freshCache(s.now().UTC()); cache != nil {
		return cache, nil
	}
	select {
	case s.refreshGate <- struct{}{}:
		defer func() { <-s.refreshGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if cache := s.freshCache(now); cache != nil {
		return cache, nil
	}

	updateTime, err := s.repo.GetUpdateTime(ctx)
	if err != nil {
		return s.cachedOrError(now, err)
	}
	s.mu.Lock()
	if s.cache != nil && !s.cache.invalidated && updateTime == s.cache.updateTime {
		s.cache.checkedAt = now
		s.cache.verifiedAt = now
		s.cache.refreshErr = nil
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

func (s *AppConfigServiceImpl) freshCache(now time.Time) *appConfigCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && !s.cache.invalidated && now.Sub(s.cache.checkedAt) < s.cacheInterval() {
		return cloneAppConfigCache(s.cache)
	}
	return nil
}

func (s *AppConfigServiceImpl) cachedOrError(checkedAt time.Time, err error) (*appConfigCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		s.cache.checkedAt = checkedAt
		s.cache.refreshErr = err
		s.cache.invalidated = false
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
	s.refreshGate <- struct{}{}
	defer func() { <-s.refreshGate }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		s.cache.invalidated = true
	}
}

func buildAppConfigCache(entries map[string]domain.AppConfigEntry, checkedAt time.Time) (*appConfigCache, error) {
	updateEntry, ok := entries[domain.AppConfigUpdateTimeKey]
	if !ok || strings.TrimSpace(updateEntry.Value) == "" {
		return nil, fmt.Errorf("%w: update_time is required", ErrInvalidAppConfig)
	}
	if _, err := decisionSettingsFromEntries(entries, domain.DecisionProviderStatus{}); err != nil {
		return nil, err
	}
	general, err := generalRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	runtime, settings, err := ssoRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	opLogs, err := operationLogRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	privateMemory, err := privateMemoryRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	recall, err := recallFeedbackRuntimeConfigFromEntries(entries)
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
		opLogs:     opLogs,
		private:    privateMemory,
		recall:     recall,
		telemetry:  telemetry,
		checkedAt:  checkedAt,
		verifiedAt: checkedAt,
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

func ssoRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (accessservice.SSORuntimeConfig, domain.SSOConfigSettings, error) {
	values := make(map[string]string, len(editableSSOConfigKeys()))
	for _, key := range editableSSOConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}

	normalized, err := normalizeSSOConfigValues(values)
	if err != nil {
		return accessservice.SSORuntimeConfig{}, domain.SSOConfigSettings{}, err
	}

	entitlementTTL, entitlementEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOEntitlementCacheTTLSeconds], accessservice.DefaultSSOEntitlementCacheTTL)
	sessionTTL, sessionEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOSessionTTLSeconds], accessservice.DefaultSSOSessionTTL)
	stateTTL, stateEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOStateTTLSeconds], accessservice.DefaultSSOStateTTL)
	httpTimeout, httpEffective := ssoConfigSeconds(normalized[domain.AppConfigSSOHTTPTimeoutSeconds], accessservice.DefaultSSOHTTPTimeout)
	cookieSecureDefault := defaultSSOCookieSecure(normalized[domain.AppConfigSSOPublicBaseURL])
	cookieSecure, cookieEffective := ssoConfigBool(normalized[domain.AppConfigSSOCookieSecure], cookieSecureDefault)

	runtime := accessservice.SSORuntimeConfig{
		PublicBaseURL:        normalized[domain.AppConfigSSOPublicBaseURL],
		MCPPublicBaseURL:     normalized[domain.AppConfigMCPPublicBaseURL],
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
		ssoConfigItem(entries, domain.AppConfigMCPPublicBaseURL, normalized[domain.AppConfigMCPPublicBaseURL]),
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

func normalizeStrictHHMM(key, value string) (string, error) {
	parsed, err := time.Parse("15:04", value)
	canonical := err == nil && parsed.Format("15:04") == value
	legacySingleDigitHour := err == nil && len(value) == 4 && parsed.Format("15:04") == "0"+value
	if err != nil || (!canonical && !legacySingleDigitHour) {
		return "", fmt.Errorf("%w: %s must use strict HH:MM", ErrInvalidAppConfig, key)
	}
	return parsed.Format("15:04"), nil
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
		case domain.AppConfigMCPPublicBaseURL:
			normalizedValue, err := normalizeMCPPublicBaseURL(trimmed)
			if err != nil {
				return nil, fmt.Errorf("%w: %s", ErrInvalidAppConfig, err)
			}
			normalized[key] = normalizedValue
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
	copy.opLogs.Items = append([]domain.OperationLogConfigItem(nil), cache.opLogs.Items...)
	copy.private.Items = append([]domain.PrivateMemoryConfigItem(nil), cache.private.Items...)
	copy.recall.Items = append([]domain.RecallFeedbackConfigItem(nil), cache.recall.Items...)
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
	_ = s.audit.Append(ctx, accessservice.AuditLogEntry{
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
