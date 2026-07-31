package dreamservice

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// EffectiveDreamingConfig resolves the global schedule plus an optional team
// enablement override. A disabled global schedule always wins; force-enable
// applies only when the global schedule is enabled.
func EffectiveDreamingConfig(global domain.DreamingRuntimeConfig, teamConfig map[string]any) (EffectiveConfig, error) {
	runtime := normalizeRuntimeConfig(global)
	cfg := EffectiveConfig{
		DreamingRuntimeConfig: runtime,
		TeamEnabled:           runtime.Enabled,
		Source:                "global",
	}
	if !runtime.Enabled {
		cfg.Enabled = false
		cfg.TeamEnabled = false
		cfg.Source = "global"
	} else if runtime.ForceEnabled {
		cfg.Enabled = true
		cfg.TeamEnabled = true
		cfg.Source = "global_force"
	} else if dreaming, ok := nestedMap(teamConfig, "dreaming"); ok {
		if enabled, ok := boolFromAny(dreaming["enabled"]); ok {
			cfg.Enabled = enabled
			cfg.TeamEnabled = enabled
			cfg.Source = "team"
		}
	}
	if err := validateEffectiveConfig(cfg); err != nil {
		return EffectiveConfig{}, err
	}
	return cfg, nil
}

func (s *service) EffectiveConfig(ctx context.Context, teamID string) (EffectiveConfig, error) {
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		teamID = actor.TeamID.String()
	}
	return s.effectiveConfigForTeam(ctx, teamID)
}

func (s *service) effectiveConfigForTeam(ctx context.Context, teamID string) (EffectiveConfig, error) {
	return resolveEffectiveConfig(ctx, teamID, s.deps.AppConfig, s.deps.Profiles)
}

func resolveEffectiveConfig(
	ctx context.Context,
	teamID string,
	appConfig AppConfig,
	profiles ProfileService,
) (EffectiveConfig, error) {
	global, err := globalDreamingConfig(ctx, appConfig)
	if err != nil {
		return EffectiveConfig{}, err
	}
	var teamConfig map[string]any
	if profiles != nil {
		team, err := parseTeamID(teamID)
		if err != nil {
			return EffectiveConfig{}, err
		}
		p, err := profiles.GetByID(ctx, team)
		if err != nil {
			return EffectiveConfig{}, err
		}
		if p != nil {
			teamConfig = p.Config
		}
	}
	return EffectiveDreamingConfig(global, teamConfig)
}

func resolveEffectiveTeamConfig(
	ctx context.Context,
	teamID string,
	appConfig AppConfig,
	teams TeamConfigService,
) (EffectiveConfig, error) {
	global, err := globalDreamingConfig(ctx, appConfig)
	if err != nil {
		return EffectiveConfig{}, err
	}
	var teamConfig map[string]any
	if teams != nil {
		teamUUID, err := uuid.Parse(teamID)
		if err != nil {
			return EffectiveConfig{}, fmt.Errorf("dreaming config: invalid team id: %w", err)
		}
		team, err := teams.GetByID(ctx, teamUUID)
		if err != nil {
			return EffectiveConfig{}, err
		}
		if team != nil {
			teamConfig = team.Config
		}
	}
	return EffectiveDreamingConfig(global, teamConfig)
}

func globalDreamingConfig(ctx context.Context, appConfig AppConfig) (domain.DreamingRuntimeConfig, error) {
	if appConfig == nil {
		return domain.DreamingRuntimeConfig{}, nil
	}
	return appConfig.DreamingRuntimeConfig(ctx)
}

func normalizeRuntimeConfig(cfg domain.DreamingRuntimeConfig) domain.DreamingRuntimeConfig {
	if strings.TrimSpace(cfg.StartTimeLocal) == "" {
		cfg.StartTimeLocal = DefaultStartTimeLocal
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		cfg.Timezone = DefaultTimezone
	}
	if cfg.MaxOutputs <= 0 {
		cfg.MaxOutputs = DefaultMaxOutputs
	}
	return cfg
}

func validateEffectiveConfig(cfg EffectiveConfig) error {
	if _, err := time.Parse("15:04", cfg.StartTimeLocal); err != nil {
		return fmt.Errorf("dreaming config: start_time_local must use HH:MM")
	}
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return fmt.Errorf("dreaming config: invalid timezone %q", cfg.Timezone)
	}
	if cfg.MaxOutputs <= 0 || cfg.MaxOutputs > 50 {
		return fmt.Errorf("dreaming config: max_outputs must be between 1 and 50")
	}
	return nil
}

func nestedMap(parent map[string]any, key string) (map[string]any, bool) {
	if parent == nil {
		return nil, false
	}
	raw, ok := parent[key]
	if !ok || raw == nil {
		return nil, false
	}
	switch v := raw.(type) {
	case map[string]any:
		return v, true
	case map[string]string:
		out := make(map[string]any, len(v))
		for k, value := range v {
			out[k] = value
		}
		return out, true
	case json.RawMessage:
		var out map[string]any
		if err := json.Unmarshal(v, &out); err == nil {
			return out, true
		}
	}
	return nil, false
}

func boolFromAny(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		if strings.TrimSpace(v) == "" {
			return false, false
		}
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return false, false
	}
}
