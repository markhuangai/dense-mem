package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (s *AppConfigServiceImpl) GetRecallFeedbackSettings(ctx context.Context) (*domain.RecallFeedbackConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.recall
	settings.Items = append([]domain.RecallFeedbackConfigItem(nil), cache.recall.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdateRecallFeedbackSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.RecallFeedbackConfigSettings, error) {
	normalized, err := normalizeRecallFeedbackConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetRecallFeedbackSettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetRecallFeedbackSettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "recall_feedback", actorRole, clientIP, correlationID, recallFeedbackSettingsPayload(before), recallFeedbackSettingsPayload(updated), map[string]any{"section": "recall_feedback"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.RecallFeedbackRuntimeConfig{}, err
	}
	return cache.recall.Effective, nil
}

func recallFeedbackRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.RecallFeedbackConfigSettings, error) {
	values := make(map[string]string, len(editableRecallFeedbackConfigKeys()))
	for _, key := range editableRecallFeedbackConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizeRecallFeedbackConfigValues(values)
	if err != nil {
		return domain.RecallFeedbackConfigSettings{}, err
	}
	enabled, enabledEffective := recallFeedbackConfigBool(normalized[domain.AppConfigRecallFeedbackEnabled], false)
	runtime := domain.RecallFeedbackRuntimeConfig{Enabled: enabled}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.RecallFeedbackConfigItem{
		recallFeedbackConfigItem(entries, domain.AppConfigRecallFeedbackEnabled, enabledEffective),
	}
	return domain.RecallFeedbackConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func normalizeRecallFeedbackConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editableRecallFeedbackConfigKeys()))
	for _, key := range editableRecallFeedbackConfigKeys() {
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
		case domain.AppConfigRecallFeedbackEnabled:
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return nil, fmt.Errorf("%w: RECALL_FEEDBACK_ENABLED must be true or false", ErrInvalidAppConfig)
				}
				trimmed = strconv.FormatBool(parsed)
			}
		}
		normalized[key] = trimmed
	}
	return normalized, nil
}

func editableRecallFeedbackConfigKeys() []string {
	return []string{
		domain.AppConfigRecallFeedbackEnabled,
	}
}

func recallFeedbackConfigBool(value string, fallback bool) (bool, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.FormatBool(fallback)
	}
	parsed, _ := strconv.ParseBool(value)
	return parsed, strconv.FormatBool(parsed)
}

func recallFeedbackConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.RecallFeedbackConfigItem {
	entry := entries[key]
	return domain.RecallFeedbackConfigItem{
		Key:            key,
		Value:          strings.TrimSpace(entry.Value),
		EffectiveValue: effective,
		UpdatedAt:      entry.UpdatedAt,
	}
}
