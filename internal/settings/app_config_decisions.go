package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
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

func NewAppConfigServiceWithDecisions(repo settingscontract.AppConfigRepository, audit ConfigAuditPreparer, options DecisionSettingsOptions) *AppConfigServiceImpl {
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
	if settings.Effective.ConfigurationError != "" {
		provider := settings.Effective.Provider
		s.decisionLog(ctx, slog.LevelError, "decision.configuration.invalid", observability.String("provider", provider.Provider), observability.String("configured_model", provider.Model), observability.LogAttr{Key: "max_input_tokens", Value: provider.MaxInputTokens}, observability.LogAttr{Key: "max_question_input_tokens", Value: provider.MaxQuestionInputTokens}, observability.String("reason_code", decisionBudgetReason(provider)), observability.String("settings_revision", settings.Effective.Revision), observability.String("error", settings.Effective.ConfigurationError))
	}
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
	current, err := s.GetDecisionSettings(ctx)
	if err != nil {
		return nil, err
	}
	if current.Effective.Stale {
		return nil, fmt.Errorf("decision settings refresh failed; refusing update from stale configuration")
	}
	updated, changed, err := updateConfigSection(ctx, s,
		configSectionUpdate{"decisions", values, actorRole, clientIP, correlationID},
		func(entries map[string]domain.AppConfigEntry) (domain.DecisionConfigSettings, error) {
			projected, err := decisionSettingsFromEntries(entries, s.decisionOptions.Provider)
			if err == nil && projected.Effective.ConfigurationError != "" {
				return projected, fmt.Errorf("%w: %s", ErrInvalidAppConfig, projected.Effective.ConfigurationError)
			}
			return projected, err
		}, decisionSettingsPayload)
	if err != nil {
		s.decisionLog(ctx, slog.LevelError, "decision.settings.persistence_failed", observability.String("error", err.Error()), observability.String("transaction_outcome", "rolled_back"))
		return nil, err
	}
	updated.Effective.LastVerifiedAt = s.now().UTC()
	if changed {
		s.decisionLog(ctx, slog.LevelInfo, "decision.settings.updated", observability.String("action", "save_supplied_preferences"), observability.LogAttr{Key: "changes", Value: values}, observability.String("settings_revision", updated.Effective.Revision))
	}
	return updated, nil
}

func decisionKeys() []string {
	return []string{domain.AppConfigDecisionDeduplication, domain.AppConfigDecisionSecurity, domain.AppConfigDecisionConflict, domain.AppConfigDecisionReranking, domain.AppConfigDecisionTopicAssignment, domain.AppConfigDecisionCommunityPlacement}
}

func validateDecisionValues(values map[string]string) error {
	if len(values) == 0 {
		return fmt.Errorf("%w: at least one decision preference is required", ErrInvalidAppConfig)
	}
	for key, value := range values {
		switch key {
		case domain.AppConfigDecisionDeduplication, domain.AppConfigDecisionSecurity, domain.AppConfigDecisionConflict, domain.AppConfigDecisionReranking, domain.AppConfigDecisionTopicAssignment, domain.AppConfigDecisionCommunityPlacement:
			if value != "true" && value != "false" {
				return fmt.Errorf("%w: %s must be true or false", ErrInvalidAppConfig, key)
			}
		case domain.AppConfigDecisionMaxInputTokens, domain.AppConfigDecisionMaxQuestionInputTokens:
			if strings.TrimSpace(value) != "" {
				parsed, err := strconv.Atoi(value)
				if err != nil || parsed <= 0 {
					return fmt.Errorf("%w: %s must be a positive integer or empty", ErrInvalidAppConfig, key)
				}
			}
		default:
			return fmt.Errorf("%w: unknown decision preference %s", ErrInvalidAppConfig, key)
		}
	}
	return nil
}

func decisionSettingsFromEntries(entries map[string]domain.AppConfigEntry, provider domain.DecisionProviderStatus) (domain.DecisionConfigSettings, error) {
	settings := domain.DecisionConfigSettings{UpdateTime: entries[domain.AppConfigUpdateTimeKey].Value, Items: []domain.DecisionConfigItem{}, Effective: domain.DecisionRuntimeConfig{Provider: provider, Revision: entries[domain.AppConfigUpdateTimeKey].Value}}
	prefs, effective, overrides := &settings.Effective.Preferences, &settings.Effective.Effective, &settings.Effective.Overrides
	targets := []struct{ preference, effective, override *bool }{{&prefs.Deduplication, &effective.Deduplication, &overrides.Deduplication}, {&prefs.SecurityReview, &effective.SecurityReview, &overrides.SecurityReview}, {&prefs.ConflictReview, &effective.ConflictReview, &overrides.ConflictReview}, {&prefs.RecallReranking, &effective.RecallReranking, &overrides.RecallReranking}, {&prefs.TopicAssignment, &effective.TopicAssignment, &overrides.TopicAssignment}, {&prefs.CommunityPlacement, &effective.CommunityPlacement, &overrides.CommunityPlacement}}
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
	startupInput, startupQuestion := provider.MaxInputTokens, provider.MaxQuestionInputTokens
	for _, field := range []struct {
		key    string
		target *int
	}{
		{domain.AppConfigDecisionMaxInputTokens, &provider.MaxInputTokens},
		{domain.AppConfigDecisionMaxQuestionInputTokens, &provider.MaxQuestionInputTokens},
	} {
		entry := entries[field.key]
		if err := validateDecisionValues(map[string]string{field.key: entry.Value}); err != nil {
			return domain.DecisionConfigSettings{}, err
		}
		source := "default"
		if strings.TrimSpace(entry.Value) != "" {
			*field.target, _ = strconv.Atoi(entry.Value)
			source = "override"
		}
		settings.Items = append(settings.Items, domain.DecisionConfigItem{Key: field.key, Value: entry.Value, Source: source, UpdatedAt: entry.UpdatedAt})
	}
	if provider.Provider == "openai" && startupInput == startupQuestion && strings.TrimSpace(entries[domain.AppConfigDecisionMaxQuestionInputTokens].Value) == "" {
		provider.MaxQuestionInputTokens = provider.MaxInputTokens
	}
	settings.Effective.Provider = provider
	settings.Items[len(settings.Items)-2].EffectiveValue = strconv.Itoa(provider.MaxInputTokens)
	settings.Items[len(settings.Items)-1].EffectiveValue = strconv.Itoa(provider.MaxQuestionInputTokens)
	invalidIndex := len(settings.Items) - 2
	if provider.Configured {
		if err := config.ValidateDecisionTokenBudgets(provider.Provider, provider.LongContextEnabled, provider.MaxInputTokens, provider.MaxQuestionInputTokens); err != nil {
			settings.Effective.ConfigurationError = err.Error()
			var validation *config.ValidationError
			if errors.As(err, &validation) && validation.Field == "AI_DECISION_MAX_QUESTION_INPUT_TOKENS" {
				invalidIndex++
			}
		}
	} else if provider.MaxQuestionInputTokens > provider.MaxInputTokens {
		settings.Effective.ConfigurationError = "question input limit must not exceed request input limit"
		invalidIndex++
	}
	if settings.Effective.ConfigurationError != "" {
		settings.Items[invalidIndex].ValidationError = settings.Effective.ConfigurationError
	}
	return settings, nil
}

func decisionBudgetReason(provider domain.DecisionProviderStatus) string {
	if provider.MaxQuestionInputTokens > provider.MaxInputTokens {
		return "question_exceeds_request"
	}
	return "exceeds_provider_ceiling"
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
