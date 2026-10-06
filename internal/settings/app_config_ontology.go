package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const DefaultOntologyCadenceHours = 12
const DefaultOntologyStartTime = "03:00"
const DefaultOntologyInputTokens = 250000
const DefaultOntologyOutputTokens = 100000

func (s *AppConfigServiceImpl) GetOntologyMaintenanceSettings(ctx context.Context) (*domain.OntologyMaintenanceSettings, error) {
	entries, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	general, err := generalRuntimeConfigFromEntries(entries)
	if err != nil {
		return nil, err
	}
	value, err := ontologyRuntimeConfigFromEntries(entries, general.Effective.Timezone)
	return &value, err
}

func (s *AppConfigServiceImpl) OntologyMaintenanceRuntimeConfig(ctx context.Context) (domain.OntologyMaintenanceConfig, error) {
	settings, err := s.GetOntologyMaintenanceSettings(ctx)
	if err != nil {
		return domain.OntologyMaintenanceConfig{}, err
	}
	return settings.Effective, nil
}

func (s *AppConfigServiceImpl) UpdateOntologyMaintenanceSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.OntologyMaintenanceSettings, error) {
	normalized, err := normalizeOntologyConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, err := s.GetOntologyMaintenanceSettings(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetOntologyMaintenanceSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "ontology_maintenance", actorRole, clientIP, correlationID, ontologySettingsPayload(before), ontologySettingsPayload(updated), map[string]any{"section": "ontology_maintenance"})
	}
	return updated, nil
}

func editableOntologyConfigKeys() []string {
	return []string{domain.AppConfigOntologyEnabled, domain.AppConfigOntologyCadenceHours, domain.AppConfigOntologyStartTime, domain.AppConfigOntologyModel, domain.AppConfigOntologyConcurrency, domain.AppConfigOntologyInputTokens, domain.AppConfigOntologyOutputTokens}
}

func normalizeOntologyConfigValues(values map[string]string) (map[string]string, error) {
	allowed := map[string]bool{}
	for _, key := range editableOntologyConfigKeys() {
		allowed[key] = true
	}
	result := map[string]string{}
	for key, raw := range values {
		if !allowed[key] {
			return nil, fmt.Errorf("%w: unknown or read-only key %s", ErrInvalidAppConfig, key)
		}
		value := strings.TrimSpace(raw)
		switch key {
		case domain.AppConfigOntologyEnabled:
			if value == "" {
				value = "false"
			}
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("%w: %s must be true or false", ErrInvalidAppConfig, key)
			}
			value = strconv.FormatBool(parsed)
		case domain.AppConfigOntologyStartTime:
			if value == "" {
				value = DefaultOntologyStartTime
			}
			var err error
			value, err = normalizeStrictHHMM(key, value)
			if err != nil {
				return nil, err
			}
		case domain.AppConfigOntologyModel:
			if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, fmt.Errorf("%w: invalid maintenance model", ErrInvalidAppConfig)
			}
		default:
			fallback, maximum := int64(1), int64(8)
			switch key {
			case domain.AppConfigOntologyCadenceHours:
				fallback, maximum = DefaultOntologyCadenceHours, 24
			case domain.AppConfigOntologyInputTokens:
				fallback, maximum = DefaultOntologyInputTokens, 1000000000
			case domain.AppConfigOntologyOutputTokens:
				fallback, maximum = DefaultOntologyOutputTokens, 1000000000
			}
			if value == "" {
				value = strconv.FormatInt(fallback, 10)
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 1 || parsed > maximum || (key == domain.AppConfigOntologyCadenceHours && parsed != 12 && parsed != 24) {
				return nil, fmt.Errorf("%w: invalid maintenance limit %s", ErrInvalidAppConfig, key)
			}
			value = strconv.FormatInt(parsed, 10)
		}
		result[key] = value
	}
	return result, nil
}

func ontologyRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry, timezone string) (domain.OntologyMaintenanceSettings, error) {
	values := map[string]string{}
	for _, key := range editableOntologyConfigKeys() {
		values[key] = entries[key].Value
	}
	values, err := normalizeOntologyConfigValues(values)
	if err != nil {
		return domain.OntologyMaintenanceSettings{}, err
	}
	integer := func(key string) int { n, _ := strconv.Atoi(values[key]); return n }
	enabled, _ := strconv.ParseBool(values[domain.AppConfigOntologyEnabled])
	policy := domain.OntologyMaintenanceConfig{Enabled: enabled, CadenceHours: integer(domain.AppConfigOntologyCadenceHours), StartTimeLocal: values[domain.AppConfigOntologyStartTime], Timezone: timezone, Model: values[domain.AppConfigOntologyModel], MaxConcurrency: integer(domain.AppConfigOntologyConcurrency), InputTokens: int64(integer(domain.AppConfigOntologyInputTokens)), OutputTokens: int64(integer(domain.AppConfigOntologyOutputTokens)), SettingsVersion: entries[domain.AppConfigUpdateTimeKey].Value}
	items := []domain.OntologyMaintenanceConfigItem{}
	for _, key := range editableOntologyConfigKeys() {
		entry := entries[key]
		items = append(items, domain.OntologyMaintenanceConfigItem{Key: key, Value: strings.TrimSpace(entry.Value), EffectiveValue: values[key], UpdatedAt: entry.UpdatedAt})
	}
	return domain.OntologyMaintenanceSettings{UpdateTime: policy.SettingsVersion, Items: items, Effective: policy}, nil
}

func ontologySettingsPayload(settings *domain.OntologyMaintenanceSettings) map[string]any {
	items := []map[string]string{}
	for _, item := range settings.Items {
		items = append(items, map[string]string{"key": item.Key, "value": item.Value})
	}
	sortPayloadItems(items)
	return map[string]any{"update_time": settings.UpdateTime, "items": items}
}
