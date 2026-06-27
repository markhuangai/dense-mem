package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (s *AppConfigServiceImpl) GetEvaluationSettings(ctx context.Context) (*domain.EvaluationConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.evaluation
	settings.Items = append([]domain.EvaluationConfigItem(nil), cache.evaluation.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateEvaluationSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.EvaluationConfigSettings, error) {
	normalized, err := normalizeEvaluationConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetEvaluationSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetEvaluationSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "evaluation", actorRole, clientIP, correlationID, evaluationSettingsPayload(before), evaluationSettingsPayload(updated), map[string]any{"section": "evaluation"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) EvaluationRuntimeConfig(ctx context.Context) (domain.EvaluationRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.EvaluationRuntimeConfig{}, err
	}
	return cache.evaluation.Effective, nil
}

func evaluationRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.EvaluationConfigSettings, error) {
	values := make(map[string]string, len(editableEvaluationConfigKeys()))
	for _, key := range editableEvaluationConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizeEvaluationConfigValues(values)
	if err != nil {
		return domain.EvaluationConfigSettings{}, err
	}
	enabled, enabledEffective := evaluationConfigBool(normalized[domain.AppConfigEvaluationModeEnabled], false)
	maxPageSize, maxPageSizeEffective := evaluationConfigInt(normalized[domain.AppConfigEvaluationExportMaxPage], DefaultEvaluationExportMaxPageSize)
	runtime := domain.EvaluationRuntimeConfig{Enabled: enabled, ExportMaxPageSize: maxPageSize}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.EvaluationConfigItem{
		evaluationConfigItem(entries, domain.AppConfigEvaluationModeEnabled, enabledEffective),
		evaluationConfigItem(entries, domain.AppConfigEvaluationExportMaxPage, maxPageSizeEffective),
	}
	return domain.EvaluationConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func normalizeEvaluationConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableEvaluationConfigKeys()))
	for _, key := range editableEvaluationConfigKeys() {
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
		case domain.AppConfigEvaluationModeEnabled:
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return nil, fmt.Errorf("%w: EVALUATION_MODE_ENABLED must be true or false", ErrInvalidAppConfig)
				}
				trimmed = strconv.FormatBool(parsed)
			}
		case domain.AppConfigEvaluationExportMaxPage:
			if trimmed == "" {
				trimmed = strconv.Itoa(DefaultEvaluationExportMaxPageSize)
			}
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed < 1 || parsed > 500 {
				return nil, fmt.Errorf("%w: EVALUATION_EXPORT_MAX_PAGE_SIZE must be between 1 and 500", ErrInvalidAppConfig)
			}
			trimmed = strconv.Itoa(parsed)
		}
		normalized[key] = trimmed
	}
	return normalized, nil
}

func editableEvaluationConfigKeys() []string {
	return []string{
		domain.AppConfigEvaluationModeEnabled,
		domain.AppConfigEvaluationExportMaxPage,
	}
}

func evaluationConfigBool(value string, fallback bool) (bool, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.FormatBool(fallback)
	}
	parsed, _ := strconv.ParseBool(value)
	return parsed, strconv.FormatBool(parsed)
}

func evaluationConfigInt(value string, fallback int) (int, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(fallback)
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, strconv.Itoa(parsed)
}

func evaluationConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.EvaluationConfigItem {
	entry := entries[key]
	return domain.EvaluationConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}
