package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const maxTelemetryCostUSDPerMillionTokens = 1_000_000

func (s *AppConfigServiceImpl) GetTelemetryPricingSettings(ctx context.Context) (*domain.TelemetryPricingConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.telemetry
	settings.Items = append([]domain.TelemetryPricingConfigItem(nil), cache.telemetry.Items...)
	settings.Effective = cloneTelemetryPricingRuntimeConfig(cache.telemetry.Effective)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateTelemetryPricingSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.TelemetryPricingConfigSettings, error) {
	normalized, err := normalizeTelemetryPricingConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetTelemetryPricingSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetTelemetryPricingSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "telemetry_pricing", actorRole, clientIP, correlationID, telemetryPricingSettingsPayload(before), telemetryPricingSettingsPayload(updated), map[string]any{"section": "telemetry_pricing"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) TelemetryPricingRuntimeConfig(ctx context.Context) (domain.TelemetryPricingRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.TelemetryPricingRuntimeConfig{}, err
	}
	return cloneTelemetryPricingRuntimeConfig(cache.telemetry.Effective), nil
}

// CachedTelemetryPricingRuntimeConfig returns the latest in-process rate card
// without refreshing configuration from storage. Provider paths use this
// snapshot so telemetry can never add a database round trip to a model call.
func (s *AppConfigServiceImpl) CachedTelemetryPricingRuntimeConfig() (domain.TelemetryPricingRuntimeConfig, bool) {
	if s == nil {
		return domain.TelemetryPricingRuntimeConfig{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		return domain.TelemetryPricingRuntimeConfig{}, false
	}
	return cloneTelemetryPricingRuntimeConfig(s.cache.telemetry.Effective), true
}

func telemetryPricingRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.TelemetryPricingConfigSettings, error) {
	runtime := domain.TelemetryPricingRuntimeConfig{}
	items := make([]domain.TelemetryPricingConfigItem, 0, len(editableTelemetryPricingConfigKeys()))
	for _, key := range editableTelemetryPricingConfigKeys() {
		normalized, err := normalizeTelemetryPricingConfigValue(key, entries[key].Value)
		if err != nil {
			items = append(items, telemetryPricingConfigItem(entries, key, "", strings.TrimPrefix(err.Error(), ErrInvalidAppConfig.Error()+": ")))
			continue
		}
		price := telemetryPricePointer(normalized)
		switch key {
		case domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens:
			runtime.VerifierInputUSDPerMillionTokens = price
		case domain.AppConfigTelemetryCostVerifierOutputUSDPerMillionTokens:
			runtime.VerifierOutputUSDPerMillionTokens = price
		case domain.AppConfigTelemetryCostEmbeddingInputUSDPerMillionTokens:
			runtime.EmbeddingInputUSDPerMillionTokens = price
		}
		items = append(items, telemetryPricingConfigItem(entries, key, normalized, ""))
	}
	return domain.TelemetryPricingConfigSettings{
		UpdateTime: entries[domain.AppConfigUpdateTimeKey].Value,
		Items:      items,
		Effective:  runtime,
	}, nil
}

func normalizeTelemetryPricingConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableTelemetryPricingConfigKeys()))
	for _, key := range editableTelemetryPricingConfigKeys() {
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
		normalizedValue, err := normalizeTelemetryPricingConfigValue(key, value)
		if err != nil {
			return nil, err
		}
		normalized[key] = normalizedValue
	}
	return normalized, nil
}

func normalizeTelemetryPricingConfigValue(key, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > maxTelemetryCostUSDPerMillionTokens {
		return "", fmt.Errorf("%w: %s must be a number between 0 and %d", ErrInvalidAppConfig, key, maxTelemetryCostUSDPerMillionTokens)
	}
	return strconv.FormatFloat(parsed, 'f', -1, 64), nil
}

func editableTelemetryPricingConfigKeys() []string {
	return []string{
		domain.AppConfigTelemetryCostVerifierInputUSDPerMillionTokens,
		domain.AppConfigTelemetryCostVerifierOutputUSDPerMillionTokens,
		domain.AppConfigTelemetryCostEmbeddingInputUSDPerMillionTokens,
	}
}

func telemetryPricePointer(value string) *float64 {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, _ := strconv.ParseFloat(value, 64)
	return &parsed
}

func telemetryPricingConfigItem(entries map[string]domain.AppConfigEntry, key, effective, validationError string) domain.TelemetryPricingConfigItem {
	entry := entries[key]
	return domain.TelemetryPricingConfigItem{
		Key:             key,
		Value:           strings.TrimSpace(entry.Value),
		EffectiveValue:  effective,
		ValidationError: validationError,
		UpdatedAt:       entry.UpdatedAt,
	}
}

func cloneTelemetryPricingRuntimeConfig(config domain.TelemetryPricingRuntimeConfig) domain.TelemetryPricingRuntimeConfig {
	copyPrice := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	return domain.TelemetryPricingRuntimeConfig{
		VerifierInputUSDPerMillionTokens:  copyPrice(config.VerifierInputUSDPerMillionTokens),
		VerifierOutputUSDPerMillionTokens: copyPrice(config.VerifierOutputUSDPerMillionTokens),
		EmbeddingInputUSDPerMillionTokens: copyPrice(config.EmbeddingInputUSDPerMillionTokens),
	}
}

func telemetryPricingSettingsPayload(settings *domain.TelemetryPricingConfigSettings) map[string]any {
	if settings == nil {
		return nil
	}
	return map[string]any{
		"update_time": settings.UpdateTime,
		"items":       settings.Items,
		"effective":   settings.Effective,
	}
}
