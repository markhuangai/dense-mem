package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

const DefaultTopicCadenceHours = 12
const DefaultTopicStartTime = "03:00"

func (s *AppConfigServiceImpl) GetTopicMaintenanceSettings(ctx context.Context) (*domain.TopicMaintenanceSettings, error) {
	cache, err := s.loadCache(ctx, true)
	if err != nil {
		return nil, err
	}
	if cache.refreshErr != nil {
		return nil, fmt.Errorf("verify current topic maintenance settings: %w", cache.refreshErr)
	}
	value, err := topicMaintenanceSettingsFromEntries(cache.entries)
	return &value, err
}

func topicMaintenanceSettingsFromEntries(entries map[string]domain.AppConfigEntry) (domain.TopicMaintenanceSettings, error) {
	general, err := generalRuntimeConfigFromEntries(entries)
	if err != nil {
		return domain.TopicMaintenanceSettings{}, err
	}
	return topicMaintenanceConfigFromEntries(entries, general.Effective.Timezone)
}

func (s *AppConfigServiceImpl) TopicMaintenanceRuntimeConfig(ctx context.Context) (domain.TopicMaintenanceConfig, error) {
	settings, err := s.GetTopicMaintenanceSettings(ctx)
	if err != nil {
		return domain.TopicMaintenanceConfig{}, err
	}
	return settings.Effective, nil
}

func (s *AppConfigServiceImpl) UpdateTopicMaintenanceSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.TopicMaintenanceSettings, error) {
	normalized, err := normalizeTopicMaintenanceConfigValues(values)
	if err != nil {
		return nil, err
	}
	updated, _, err := updateConfigSection(ctx, s,
		configSectionUpdate{"topic_maintenance", normalized, actorRole, clientIP, correlationID}, topicMaintenanceSettingsFromEntries, topicMaintenanceSettingsPayload)
	return updated, err
}

func editableTopicMaintenanceConfigKeys() []string {
	return []string{domain.AppConfigTopicMaintenanceEnabled, domain.AppConfigTopicMaintenanceCadenceHours, domain.AppConfigTopicMaintenanceStartTime, domain.AppConfigTopicMaintenanceModel, domain.AppConfigTopicMaintenanceConcurrency, domain.AppConfigTopicMaintenanceTeamConcurrency, domain.AppConfigTopicMaintenanceTeamOverrides, domain.AppConfigTopicMaintenanceContextWindow}
}

func normalizeTopicMaintenanceConfigValues(values map[string]string) (map[string]string, error) {
	allowed := map[string]bool{}
	for _, key := range editableTopicMaintenanceConfigKeys() {
		allowed[key] = true
	}
	result := map[string]string{}
	for key, raw := range values {
		if !allowed[key] {
			return nil, fmt.Errorf("%w: unknown or read-only key %s", ErrInvalidAppConfig, key)
		}
		value := strings.TrimSpace(raw)
		switch key {
		case domain.AppConfigTopicMaintenanceTeamOverrides:
			overrides, err := parseTopicTeamOverrides(value)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(overrides)
			if err != nil {
				return nil, err
			}
			value = string(encoded)
		case domain.AppConfigTopicMaintenanceEnabled:
			if value == "" {
				value = "false"
			}
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("%w: %s must be true or false", ErrInvalidAppConfig, key)
			}
			value = strconv.FormatBool(parsed)
		case domain.AppConfigTopicMaintenanceStartTime:
			if value == "" {
				value = DefaultTopicStartTime
			}
			var err error
			value, err = normalizeStrictHHMM(key, value)
			if err != nil {
				return nil, err
			}
		case domain.AppConfigTopicMaintenanceContextWindow:
			if value != "" {
				parsed, err := strconv.ParseInt(value, 10, 64)
				if err != nil || parsed < 1 || parsed > 1000000000 {
					return nil, fmt.Errorf("%w: context window must be 1..1000000000 tokens", ErrInvalidAppConfig)
				}
				value = strconv.FormatInt(parsed, 10)
			}
		case domain.AppConfigTopicMaintenanceModel:
			if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, fmt.Errorf("%w: invalid maintenance model", ErrInvalidAppConfig)
			}
		default:
			fallback, maximum := int64(1), int64(8)
			switch key {
			case domain.AppConfigTopicMaintenanceTeamConcurrency:
				fallback = domain.DefaultTopicTeamConcurrency
			case domain.AppConfigTopicMaintenanceCadenceHours:
				fallback, maximum = DefaultTopicCadenceHours, 24
			}
			if value == "" {
				value = strconv.FormatInt(fallback, 10)
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 1 || parsed > maximum || (key == domain.AppConfigTopicMaintenanceCadenceHours && parsed != 12 && parsed != 24) {
				return nil, fmt.Errorf("%w: invalid maintenance limit %s", ErrInvalidAppConfig, key)
			}
			value = strconv.FormatInt(parsed, 10)
		}
		result[key] = value
	}
	return result, nil
}

func topicMaintenanceConfigFromEntries(entries map[string]domain.AppConfigEntry, timezone string) (domain.TopicMaintenanceSettings, error) {
	values := map[string]string{}
	for _, key := range editableTopicMaintenanceConfigKeys() {
		values[key] = entries[key].Value
	}
	values, err := normalizeTopicMaintenanceConfigValues(values)
	if err != nil {
		return domain.TopicMaintenanceSettings{}, err
	}
	integer := func(key string) int { n, _ := strconv.Atoi(values[key]); return n }
	enabled, _ := strconv.ParseBool(values[domain.AppConfigTopicMaintenanceEnabled])
	policy := domain.TopicMaintenanceConfig{Enabled: enabled, CadenceHours: integer(domain.AppConfigTopicMaintenanceCadenceHours), StartTimeLocal: values[domain.AppConfigTopicMaintenanceStartTime], Timezone: timezone, Model: values[domain.AppConfigTopicMaintenanceModel], MaxConcurrency: integer(domain.AppConfigTopicMaintenanceConcurrency), ContextWindowTokens: int64(integer(domain.AppConfigTopicMaintenanceContextWindow)), SettingsVersion: entries[domain.AppConfigUpdateTimeKey].Value}
	policy.TeamMaxConcurrency = integer(domain.AppConfigTopicMaintenanceTeamConcurrency)
	policy.TeamConcurrencyOverrides, err = parseTopicTeamOverrides(values[domain.AppConfigTopicMaintenanceTeamOverrides])
	if err != nil {
		return domain.TopicMaintenanceSettings{}, err
	}
	items := []domain.TopicMaintenanceConfigItem{}
	for _, key := range editableTopicMaintenanceConfigKeys() {
		entry := entries[key]
		items = append(items, domain.TopicMaintenanceConfigItem{Key: key, Value: strings.TrimSpace(entry.Value), EffectiveValue: values[key], UpdatedAt: entry.UpdatedAt})
	}
	return domain.TopicMaintenanceSettings{UpdateTime: policy.SettingsVersion, Items: items, Effective: policy}, nil
}

func parseTopicTeamOverrides(raw string) ([]domain.TopicTeamConcurrencyOverride, error) {
	if raw == "" {
		return []domain.TopicTeamConcurrencyOverride{}, nil
	}
	var overrides []domain.TopicTeamConcurrencyOverride
	if err := jsonstrict.Decode(strings.NewReader(raw), &overrides, 16384); err != nil || overrides == nil || len(overrides) > 128 {
		return nil, fmt.Errorf("%w: team overrides require an array of at most 128 entries and 16384 bytes", ErrInvalidAppConfig)
	}
	seen := map[string]bool{}
	for _, override := range overrides {
		id, err := uuid.Parse(override.TeamID)
		if err != nil || id == uuid.Nil || id.String() != override.TeamID || seen[override.TeamID] || override.MaxConcurrency < 1 || override.MaxConcurrency > 8 {
			return nil, fmt.Errorf("%w: team overrides require unique canonical team UUIDs and limits 1..8", ErrInvalidAppConfig)
		}
		seen[override.TeamID] = true
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].TeamID < overrides[j].TeamID })
	return overrides, nil
}

func topicMaintenanceSettingsPayload(settings *domain.TopicMaintenanceSettings) map[string]any {
	items := []map[string]string{}
	for _, item := range settings.Items {
		items = append(items, map[string]string{"key": item.Key, "value": item.Value})
	}
	sortPayloadItems(items)
	return map[string]any{"update_time": settings.UpdateTime, "items": items}
}
