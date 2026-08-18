package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const DefaultOperationLogRetentionDays = 30
const DefaultPrivateMemoryRetentionDays = 0

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

func (s *AppConfigServiceImpl) GetPrivateMemorySettings(ctx context.Context) (*domain.PrivateMemoryConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return nil, err
	}
	settings := cache.private
	settings.Items = append([]domain.PrivateMemoryConfigItem(nil), cache.private.Items...)
	return &settings, nil
}

func (s *AppConfigServiceImpl) UpdatePrivateMemorySettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.PrivateMemoryConfigSettings, error) {
	normalized, err := normalizePrivateMemoryConfigValues(values)
	if err != nil {
		return nil, err
	}
	before, _ := s.GetPrivateMemorySettings(ctx)
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, normalized, now.Format(time.RFC3339Nano), now)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	updated, err := s.GetPrivateMemorySettings(ctx)
	if err != nil {
		return nil, err
	}
	if changed {
		s.appendAudit("APP_CONFIG_UPDATE", "app_config", "private_memory", actorRole, clientIP, correlationID, privateMemorySettingsPayload(before), privateMemorySettingsPayload(updated), map[string]any{"section": "private_memory"})
	}
	return updated, nil
}

func (s *AppConfigServiceImpl) PrivateMemoryRuntimeConfig(ctx context.Context) (domain.PrivateMemoryRuntimeConfig, error) {
	cache, err := s.currentCache(ctx)
	if err != nil {
		return domain.PrivateMemoryRuntimeConfig{}, err
	}
	return cache.private.Effective, nil
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

func privateMemoryRuntimeConfigFromEntries(entries map[string]domain.AppConfigEntry) (domain.PrivateMemoryConfigSettings, error) {
	values := make(map[string]string, len(editablePrivateMemoryConfigKeys()))
	for _, key := range editablePrivateMemoryConfigKeys() {
		values[key] = strings.TrimSpace(entries[key].Value)
	}
	normalized, err := normalizePrivateMemoryConfigValues(values)
	if err != nil {
		return domain.PrivateMemoryConfigSettings{}, err
	}
	retentionDays, retentionEffective := privateMemoryConfigInt(normalized[domain.AppConfigPrivateMemoryRetentionDays], DefaultPrivateMemoryRetentionDays)
	runtime := domain.PrivateMemoryRuntimeConfig{RetentionDays: retentionDays}
	updateTime := entries[domain.AppConfigUpdateTimeKey].Value
	items := []domain.PrivateMemoryConfigItem{
		privateMemoryConfigItem(entries, domain.AppConfigPrivateMemoryRetentionDays, retentionEffective),
	}
	return domain.PrivateMemoryConfigSettings{UpdateTime: updateTime, Items: items, Effective: runtime}, nil
}

func normalizePrivateMemoryConfigValues(values map[string]string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(editablePrivateMemoryConfigKeys()))
	for _, key := range editablePrivateMemoryConfigKeys() {
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
		if trimmed == "" {
			trimmed = strconv.Itoa(DefaultPrivateMemoryRetentionDays)
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil || parsed < 0 || parsed > 36500 {
			return nil, fmt.Errorf("%w: PRIVATE_MEMORY_RETENTION_DAYS must be between 0 and 36500", ErrInvalidAppConfig)
		}
		normalized[key] = strconv.Itoa(parsed)
	}
	return normalized, nil
}

func editableOperationLogConfigKeys() []string {
	return []string{
		domain.AppConfigOperationLogRetentionDays,
	}
}

func editablePrivateMemoryConfigKeys() []string {
	return []string{domain.AppConfigPrivateMemoryRetentionDays}
}

func operationLogConfigInt(value string, fallback int) (int, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(fallback)
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, strconv.Itoa(parsed)
}

func privateMemoryConfigInt(value string, fallback int) (int, string) {
	if strings.TrimSpace(value) == "" {
		return fallback, strconv.Itoa(fallback)
	}
	parsed, _ := strconv.Atoi(value)
	return parsed, strconv.Itoa(parsed)
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

func privateMemoryConfigItem(entries map[string]domain.AppConfigEntry, key, effective string) domain.PrivateMemoryConfigItem {
	entry := entries[key]
	return domain.PrivateMemoryConfigItem{
		Key: key, Value: strings.TrimSpace(entry.Value), EffectiveValue: effective, UpdatedAt: entry.UpdatedAt,
	}
}
