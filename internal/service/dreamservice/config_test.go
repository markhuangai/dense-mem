package dreamservice

import (
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestEffectiveDreamingConfigTeamOverridesAndForceEnable(t *testing.T) {
	global := domain.DreamingRuntimeConfig{
		Enabled:           true,
		ForceEnabled:      false,
		StartTimeLocal:    "03:00",
		Timezone:          "UTC",
		ReflectEnabled:    true,
		ReevaluateEnabled: true,
		DreamEnabled:      true,
		MaxOutputs:        5,
	}

	cfg, err := EffectiveDreamingConfig(global, map[string]any{
		"dreaming": map[string]any{
			"enabled":            false,
			"reflect_enabled":    true,
			"reevaluate_enabled": false,
			"dream_enabled":      true,
			"start_time_local":   "02:30",
			"timezone":           "America/New_York",
			"max_outputs":        float64(9),
		},
	})
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.False(t, cfg.TeamEnabled)
	require.True(t, cfg.ReflectEnabled)
	require.False(t, cfg.ReevaluateEnabled)
	require.True(t, cfg.DreamEnabled)
	require.Equal(t, "02:30", cfg.StartTimeLocal)
	require.Equal(t, "America/New_York", cfg.Timezone)
	require.Equal(t, 9, cfg.MaxOutputs)
	require.Equal(t, "team", cfg.Source)

	cfg, err = EffectiveDreamingConfig(global, map[string]any{
		"dreaming": map[string]any{
			"timezone":    "America/Los_Angeles",
			"max_outputs": 7,
		},
	})
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.True(t, cfg.TeamEnabled)
	require.Equal(t, "America/Los_Angeles", cfg.Timezone)
	require.Equal(t, 7, cfg.MaxOutputs)
	require.Equal(t, "team", cfg.Source)

	global.ForceEnabled = true
	cfg, err = EffectiveDreamingConfig(global, map[string]any{
		"dreaming": map[string]any{"enabled": false},
	})
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.True(t, cfg.TeamEnabled)
	require.Equal(t, "global_force", cfg.Source)
}

func TestEffectiveDreamingConfigRawMessageAndValidation(t *testing.T) {
	raw := json.RawMessage(`{"enabled":true,"start_time_local":"04:15","timezone":"UTC","max_outputs":"3"}`)
	cfg, err := EffectiveDreamingConfig(domain.DreamingRuntimeConfig{}, map[string]any{"dreaming": raw})
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, "04:15", cfg.StartTimeLocal)
	require.Equal(t, 3, cfg.MaxOutputs)

	_, err = EffectiveDreamingConfig(domain.DreamingRuntimeConfig{StartTimeLocal: "25:99", Timezone: "UTC", MaxOutputs: 5}, nil)
	require.ErrorContains(t, err, "start_time_local")

	_, err = EffectiveDreamingConfig(domain.DreamingRuntimeConfig{StartTimeLocal: "03:00", Timezone: "Nope/Zone", MaxOutputs: 5}, nil)
	require.ErrorContains(t, err, "invalid timezone")

	_, err = EffectiveDreamingConfig(domain.DreamingRuntimeConfig{StartTimeLocal: "03:00", Timezone: "UTC", MaxOutputs: 51}, nil)
	require.ErrorContains(t, err, "max_outputs")
}

func TestRunCycleRequestJSONTagsAcceptToolInput(t *testing.T) {
	var req RunCycleRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"reflect_enabled": false,
		"reevaluate_enabled": false,
		"dream_enabled": true,
		"max_outputs": 9
	}`), &req))

	require.NotNil(t, req.ReflectEnabled)
	require.False(t, *req.ReflectEnabled)
	require.NotNil(t, req.ReevaluateEnabled)
	require.False(t, *req.ReevaluateEnabled)
	require.NotNil(t, req.DreamEnabled)
	require.True(t, *req.DreamEnabled)
	require.Equal(t, 9, req.MaxOutputs)
}
