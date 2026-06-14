package dreamservice

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// EffectiveDreamingConfig resolves global defaults plus optional
// teams.config.dreaming overrides. Global force-enable controls whether teams
// may opt out; it does not change phase ordering.
func EffectiveDreamingConfig(global domain.DreamingRuntimeConfig, teamConfig map[string]any) (EffectiveConfig, error) {
	runtime := normalizeRuntimeConfig(global)
	cfg := EffectiveConfig{
		DreamingRuntimeConfig: runtime,
		TeamEnabled:           runtime.Enabled,
		Source:                "global",
	}
	dreaming, ok := nestedMap(teamConfig, "dreaming")
	if ok {
		cfg.Source = "team"
		if v, ok := boolFromAny(dreaming["enabled"]); ok {
			cfg.Enabled = v
			cfg.TeamEnabled = v
		}
		if v, ok := boolFromAny(dreaming["reflect_enabled"]); ok {
			cfg.ReflectEnabled = v
		}
		if v, ok := boolFromAny(dreaming["reevaluate_enabled"]); ok {
			cfg.ReevaluateEnabled = v
		}
		if v, ok := boolFromAny(dreaming["dream_enabled"]); ok {
			cfg.DreamEnabled = v
		}
		if v, ok := stringFromAny(dreaming["start_time_local"]); ok && strings.TrimSpace(v) != "" {
			cfg.StartTimeLocal = strings.TrimSpace(v)
		}
		if v, ok := stringFromAny(dreaming["timezone"]); ok && strings.TrimSpace(v) != "" {
			cfg.Timezone = strings.TrimSpace(v)
		}
		if v, ok := intFromAny(dreaming["max_outputs"]); ok {
			cfg.MaxOutputs = v
		}
	}
	if cfg.ForceEnabled {
		cfg.Enabled = true
		cfg.TeamEnabled = true
		cfg.Source = "global_force"
	}
	if err := validateEffectiveConfig(cfg); err != nil {
		return EffectiveConfig{}, err
	}
	return cfg, nil
}

func (s *service) EffectiveConfig(ctx context.Context, profileID string) (EffectiveConfig, error) {
	global := domain.DreamingRuntimeConfig{}
	if s.deps.AppConfig != nil {
		next, err := s.deps.AppConfig.DreamingRuntimeConfig(ctx)
		if err != nil {
			return EffectiveConfig{}, err
		}
		global = next
	}
	var teamConfig map[string]any
	if s.deps.Profiles != nil {
		profile, err := parseProfileID(profileID)
		if err != nil {
			return EffectiveConfig{}, err
		}
		p, err := s.deps.Profiles.GetByID(ctx, profile)
		if err != nil {
			return EffectiveConfig{}, err
		}
		if p != nil {
			teamConfig = p.Config
		}
	}
	return EffectiveDreamingConfig(global, teamConfig)
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

func stringFromAny(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true
	default:
		return "", false
	}
}

func intFromAny(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return 0, false
	}
}
