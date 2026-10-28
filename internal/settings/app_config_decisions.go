package settings

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

type DecisionConfigService interface {
	GetDecisionSettings(context.Context) (*domain.DecisionConfigSettings, error)
	UpdateDecisionSettings(context.Context, map[string]string, string, string, string) (*domain.DecisionConfigSettings, error)
	DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error)
}

type DecisionSettingsOptions struct {
	Provider domain.DecisionProviderStatus
	Logger   observability.LogProvider
}

func NewAppConfigServiceWithDecisions(repo settingscontract.AppConfigRepository, audit accessservice.AuditService, options DecisionSettingsOptions) *AppConfigServiceImpl {
	service := NewAppConfigService(repo, audit)
	service.decisionOptions = options
	return service
}

func (s *AppConfigServiceImpl) GetDecisionSettings(ctx context.Context) (*domain.DecisionConfigSettings, error) {
	cache, err := s.currentCache(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		s.decisionLog(ctx, slog.LevelError, "decision.settings.unavailable", observability.LogAttr{Key: "reason_code", Value: "no_valid_snapshot"}, observability.LogAttr{Key: "error", Value: err.Error()})
		return nil, err
	}
	settings, err := decisionSettingsFromEntries(cache.entries, s.decisionOptions.Provider)
	if err != nil {
		s.decisionLog(ctx, slog.LevelError, "decision.settings.invalid", observability.LogAttr{Key: "error", Value: err.Error()})
		return nil, err
	}
	settings.Effective.Stale = cache.refreshErr != nil
	settings.Effective.LastVerifiedAt = cache.verifiedAt
	if cache.refreshErr != nil {
		s.decisionLog(ctx, slog.LevelWarn, "decision.settings.stale", observability.LogAttr{Key: "settings_revision", Value: cache.updateTime}, observability.LogAttr{Key: "action", Value: "retain_last_valid_snapshot"}, observability.LogAttr{Key: "error", Value: cache.refreshErr.Error()}, observability.LogAttr{Key: "last_verified_at", Value: cache.verifiedAt})
	}
	return &settings, nil
}

func (s *AppConfigServiceImpl) DecisionRuntimeConfig(ctx context.Context) (domain.DecisionRuntimeConfig, error) {
	settings, err := s.GetDecisionSettings(ctx)
	if err != nil {
		return domain.DecisionRuntimeConfig{}, err
	}
	return settings.Effective, nil
}

func (s *AppConfigServiceImpl) UpdateDecisionSettings(ctx context.Context, values map[string]string, actorRole, clientIP, correlationID string) (*domain.DecisionConfigSettings, error) {
	if err := validateDecisionValues(values); err != nil {
		return nil, err
	}
	before, err := s.GetDecisionSettings(ctx)
	if err != nil {
		return nil, err
	}
	if before.Effective.Stale {
		return nil, fmt.Errorf("decision settings refresh failed; refusing update from stale configuration")
	}
	now := s.now().UTC()
	changed, err := s.repo.UpdateValues(ctx, values, now.Format(time.RFC3339Nano), now)
	if err != nil {
		s.decisionLog(ctx, slog.LevelError, "decision.settings.persistence_failed", observability.LogAttr{Key: "error", Value: err.Error()})
		return nil, err
	}
	s.invalidate()
	if changed {
		if s.audit != nil {
			err = s.audit.Append(ctx, accessservice.AuditLogEntry{Operation: "APP_CONFIG_UPDATE", EntityType: "app_config", EntityID: "decisions", ActorRole: actorRole, ClientIP: clientIP, CorrelationID: correlationID, BeforePayload: decisionSettingsPayload(before), AfterPayload: map[string]any{"supplied_changes": values}, Metadata: map[string]any{"section": "decisions"}})
			if err != nil {
				s.decisionLog(ctx, slog.LevelError, "decision.settings.audit_failed", observability.LogAttr{Key: "error", Value: err.Error()}, observability.LogAttr{Key: "action", Value: "report_persisted_change_without_audit"})
				return nil, fmt.Errorf("decision settings persisted but audit failed: %w", err)
			}
		}
		s.decisionLog(ctx, slog.LevelInfo, "decision.settings.updated", observability.LogAttr{Key: "action", Value: "save_supplied_preferences"}, observability.LogAttr{Key: "changes", Value: values}, observability.LogAttr{Key: "settings_revision", Value: now.Format(time.RFC3339Nano)})
	}
	updated, err := s.GetDecisionSettings(ctx)
	if err != nil {
		return nil, err
	}
	if updated.Effective.Stale {
		return nil, fmt.Errorf("decision settings persisted but refresh failed")
	}
	return updated, nil
}

func decisionKeys() []string {
	return []string{domain.AppConfigDecisionDeduplication, domain.AppConfigDecisionSecurity, domain.AppConfigDecisionConflict, domain.AppConfigDecisionReranking}
}

func validateDecisionValues(values map[string]string) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: at least one decision preference is required", ErrInvalidAppConfig)
	}
	for key, value := range values {
		switch key {
		case domain.AppConfigDecisionDeduplication, domain.AppConfigDecisionSecurity, domain.AppConfigDecisionConflict, domain.AppConfigDecisionReranking:
		default:
			return fmt.Errorf("%w: unknown decision preference %s", ErrInvalidAppConfig, key)
		}
		if value != "true" && value != "false" {
			return fmt.Errorf("%w: %s must be true or false", ErrInvalidAppConfig, key)
		}
	}
	return nil
}

func decisionSettingsFromEntries(entries map[string]domain.AppConfigEntry, provider domain.DecisionProviderStatus) (domain.DecisionConfigSettings, error) {
	settings := domain.DecisionConfigSettings{UpdateTime: entries[domain.AppConfigUpdateTimeKey].Value, Items: []domain.DecisionConfigItem{}, Effective: domain.DecisionRuntimeConfig{Provider: provider, Revision: entries[domain.AppConfigUpdateTimeKey].Value}}
	prefs, effective, overrides := &settings.Effective.Preferences, &settings.Effective.Effective, &settings.Effective.Overrides
	targets := []struct{ preference, effective, override *bool }{{&prefs.Deduplication, &effective.Deduplication, &overrides.Deduplication}, {&prefs.SecurityReview, &effective.SecurityReview, &overrides.SecurityReview}, {&prefs.ConflictReview, &effective.ConflictReview, &overrides.ConflictReview}, {&prefs.RecallReranking, &effective.RecallReranking, &overrides.RecallReranking}}
	for i, key := range decisionKeys() {
		entry, exists := entries[key]
		preference, source := true, "default"
		if exists {
			if err := validateDecisionValues(map[string]string{key: entry.Value}); err != nil {
				return domain.DecisionConfigSettings{}, err
			}
			preference, source = entry.Value == "true", "override"
		}
		*targets[i].preference, *targets[i].effective, *targets[i].override = preference, provider.Configured && preference, exists
		settings.Items = append(settings.Items, domain.DecisionConfigItem{Key: key, Value: entry.Value, EffectiveValue: strconv.FormatBool(provider.Configured && preference), Source: source, UpdatedAt: entry.UpdatedAt})
	}
	return settings, nil
}

func decisionSettingsPayload(settings *domain.DecisionConfigSettings) map[string]any {
	values := map[string]any{}
	if settings != nil {
		for _, item := range settings.Items {
			values[item.Key] = item.Value
		}
	}
	return values
}

func (s *AppConfigServiceImpl) decisionLog(ctx context.Context, level slog.Level, event string, attrs ...observability.LogAttr) {
	if s == nil || s.decisionOptions.Logger == nil {
		return
	}
	attrs = append(attrs, observability.LogAttr{Key: "event", Value: event})
	if logger, ok := s.decisionOptions.Logger.(observability.ContextLogProvider); ok {
		logger.LogContext(ctx, level, event, attrs...)
		return
	}
	switch level {
	case slog.LevelError:
		s.decisionOptions.Logger.Error(event, nil, attrs...)
	case slog.LevelWarn:
		s.decisionOptions.Logger.Warn(event, attrs...)
	default:
		s.decisionOptions.Logger.Info(event, attrs...)
	}
}
