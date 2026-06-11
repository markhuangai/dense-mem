package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const DefaultAppConfigCacheCheckInterval = 5 * time.Second

var ErrInvalidAppConfig = errors.New("invalid app config")

type AppConfigService interface {
	GetSSOSettings(ctx context.Context) (*domain.SSOConfigSettings, error)
	UpdateSSOSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.SSOConfigSettings, error)
	SSORuntimeConfig(ctx context.Context) (SSORuntimeConfig, error)
	GetDreamingSettings(ctx context.Context) (*domain.DreamingConfigSettings, error)
	UpdateDreamingSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.DreamingConfigSettings, error)
	DreamingRuntimeConfig(ctx context.Context) (domain.DreamingRuntimeConfig, error)
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
	sso        SSORuntimeConfig
	settings   domain.SSOConfigSettings
	dreaming   domain.DreamingConfigSettings
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

func (s *AppConfigServiceImpl) currentCache(ctx context.Context) (*appConfigCache, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("app config service is unavailable")
	}
	now := s.now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache != nil && now.Sub(s.cache.checkedAt) < s.cacheInterval() {
		return cloneAppConfigCache(s.cache), nil
	}

	updateTime, err := s.repo.GetUpdateTime(ctx)
	if err != nil {
		if s.cache != nil {
			s.cache.checkedAt = now
			return cloneAppConfigCache(s.cache), nil
		}
		return nil, err
	}
	if s.cache != nil && updateTime == s.cache.updateTime {
		s.cache.checkedAt = now
		return cloneAppConfigCache(s.cache), nil
	}

	entries, err := s.repo.List(ctx)
	if err != nil {
		if s.cache != nil {
			s.cache.checkedAt = now
			return cloneAppConfigCache(s.cache), nil
		}
		return nil, err
	}
	next, err := buildAppConfigCache(entries, now)
	if err != nil {
		if s.cache != nil {
			s.cache.checkedAt = now
			return cloneAppConfigCache(s.cache), nil
		}
		return nil, err
	}
	s.cache = next
	return cloneAppConfigCache(s.cache), nil
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
	runtime, settings, err := ssoRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	dreaming, err := dreamingRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	return &appConfigCache{
		updateTime: updateEntry.Value,
		entries:    cloneAppConfigEntries(entries),
		sso:        runtime,
		settings:   settings,
		dreaming:   dreaming,
		checkedAt:  checkedAt,
	}, nil
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
		PublicBaseURL:       normalized[domain.AppConfigSSOPublicBaseURL],
		EntitlementCacheTTL: entitlementTTL,
		SessionTTL:          sessionTTL,
		StateTTL:            stateTTL,
		CookieSecure:        cookieSecure,
		HTTPTimeout:         httpTimeout,
	}

	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.SSOConfigItem{
		ssoConfigItem(entries, domain.AppConfigSSOPublicBaseURL, normalized[domain.AppConfigSSOPublicBaseURL]),
		ssoConfigItem(entries, domain.AppConfigSSOEntitlementCacheTTLSeconds, entitlementEffective),
		ssoConfigItem(entries, domain.AppConfigSSOSessionTTLSeconds, sessionEffective),
		ssoConfigItem(entries, domain.AppConfigSSOStateTTLSeconds, stateEffective),
		ssoConfigItem(entries, domain.AppConfigSSOHTTPTimeoutSeconds, httpEffective),
		ssoConfigItem(entries, domain.AppConfigSSOCookieSecure, cookieEffective),
	}

	return runtime, domain.SSOConfigSettings{UpdateTime: updateTime, Items: items}, nil
}

func dreamingRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.DreamingConfigSettings, error) {
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
	reflectEnabled, reflectEffective := dreamingConfigBool(normalized[domain.AppConfigDreamingReflectEnabled], true)
	reevaluateEnabled, reevaluateEffective := dreamingConfigBool(normalized[domain.AppConfigDreamingReevaluateEnabled], true)
	dreamEnabled, dreamEffective := dreamingConfigBool(normalized[domain.AppConfigDreamingDreamEnabled], true)
	maxOutputs, maxOutputsEffective := dreamingConfigInt(normalized[domain.AppConfigDreamingMaxOutputs], 5)
	startTime := dreamingConfigString(normalized[domain.AppConfigDreamingStartTimeLocal], "03:00")
	timezone := dreamingConfigString(normalized[domain.AppConfigDreamingTimezone], "UTC")
	model := strings.TrimSpace(normalized[domain.AppConfigDreamingModel])

	runtime := domain.DreamingRuntimeConfig{
		Enabled:           enabled,
		ForceEnabled:      forceEnabled,
		StartTimeLocal:    startTime,
		Timezone:          timezone,
		ReflectEnabled:    reflectEnabled,
		ReevaluateEnabled: reevaluateEnabled,
		DreamEnabled:      dreamEnabled,
		Model:             model,
		MaxOutputs:        maxOutputs,
	}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.DreamingConfigItem{
		dreamingConfigItem(entries, domain.AppConfigDreamingEnabled, enabledEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingForceEnabled, forceEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingStartTimeLocal, startTime),
		dreamingConfigItem(entries, domain.AppConfigDreamingTimezone, timezone),
		dreamingConfigItem(entries, domain.AppConfigDreamingReflectEnabled, reflectEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingReevaluateEnabled, reevaluateEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingDreamEnabled, dreamEffective),
		dreamingConfigItem(entries, domain.AppConfigDreamingModel, model),
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
		case domain.AppConfigDreamingEnabled, domain.AppConfigDreamingForceEnabled,
			domain.AppConfigDreamingReflectEnabled, domain.AppConfigDreamingReevaluateEnabled,
			domain.AppConfigDreamingDreamEnabled:
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
		case domain.AppConfigDreamingTimezone:
			if trimmed == "" {
				trimmed = "UTC"
			}
			if _, err := time.LoadLocation(trimmed); err != nil {
				return nil, fmt.Errorf("%w: DREAMING_TIMEZONE is not a valid IANA timezone", ErrInvalidAppConfig)
			}
		case domain.AppConfigDreamingModel:
			if len(trimmed) > 128 {
				return nil, fmt.Errorf("%w: DREAMING_MODEL exceeds maximum length of 128", ErrInvalidAppConfig)
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
		case domain.AppConfigSSOPublicBaseURL:
			normalized[key] = strings.TrimRight(trimmed, "/")
			if normalized[key] != "" {
				parsed, err := url.Parse(normalized[key])
				if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
					return nil, fmt.Errorf("%w: SSO_PUBLIC_BASE_URL must be an absolute http or https URL", ErrInvalidAppConfig)
				}
				if parsed.RawQuery != "" || parsed.Fragment != "" {
					return nil, fmt.Errorf("%w: SSO_PUBLIC_BASE_URL must not include query or fragment", ErrInvalidAppConfig)
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

func editableSSOConfigKeys() []string {
	return []string{
		domain.AppConfigSSOPublicBaseURL,
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
		domain.AppConfigDreamingTimezone,
		domain.AppConfigDreamingReflectEnabled,
		domain.AppConfigDreamingReevaluateEnabled,
		domain.AppConfigDreamingDreamEnabled,
		domain.AppConfigDreamingModel,
		domain.AppConfigDreamingMaxOutputs,
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

func dreamingConfigString(value, fallback string) string {
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

func dreamingConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.DreamingConfigItem {
	entry := entries[key]
	return domain.DreamingConfigItem{
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
	copy.settings.Items = append([]domain.SSOConfigItem(nil), cache.settings.Items...)
	copy.dreaming.Items = append([]domain.DreamingConfigItem(nil), cache.dreaming.Items...)
	return &copy
}

func cloneAppConfigEntries(entries map[string]domain.AppConfigEntry) map[string]domain.AppConfigEntry {
	copy := make(map[string]domain.AppConfigEntry, len(entries))
	for key, value := range entries {
		copy[key] = value
	}
	return copy
}

func ssoSettingsPayload(settings *domain.SSOConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["key"] < items[j]["key"] })
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
}

func dreamingSettingsPayload(settings *domain.DreamingConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	items := make([]map[string]string, 0, len(settings.Items))
	for _, item := range settings.Items {
		items = append(items, map[string]string{
			"key":   item.Key,
			"value": item.Value,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["key"] < items[j]["key"] })
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       items,
	}
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
