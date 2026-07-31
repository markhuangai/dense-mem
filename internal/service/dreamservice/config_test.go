package dreamservice

import (
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestEffectiveDreamingConfigTeamOverridesAndForceEnable(t *testing.T) {
	global := domain.DreamingRuntimeConfig{
		Enabled:        true,
		ForceEnabled:   false,
		StartTimeLocal: "03:00",
		Timezone:       "UTC",
		MaxOutputs:     5,
	}

	cfg, err := EffectiveDreamingConfig(global, map[string]any{
		"dreaming": map[string]any{
			"enabled":          false,
			"start_time_local": "02:30",
			"timezone":         "America/New_York",
			"max_outputs":      float64(9),
		},
	})
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.False(t, cfg.TeamEnabled)
	require.Equal(t, "03:00", cfg.StartTimeLocal)
	require.Equal(t, "UTC", cfg.Timezone)
	require.Equal(t, 5, cfg.MaxOutputs)
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
	require.Equal(t, "UTC", cfg.Timezone)
	require.Equal(t, 5, cfg.MaxOutputs)
	require.Equal(t, "global", cfg.Source)

	global.ForceEnabled = true
	cfg, err = EffectiveDreamingConfig(global, map[string]any{
		"dreaming": map[string]any{"enabled": false},
	})
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.True(t, cfg.TeamEnabled)
	require.Equal(t, "global_force", cfg.Source)

	global.Enabled = false
	cfg, err = EffectiveDreamingConfig(global, map[string]any{
		"dreaming": map[string]any{"enabled": true},
	})
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.False(t, cfg.TeamEnabled)
	require.Equal(t, "global", cfg.Source)
}

func TestEffectiveDreamingConfigRawMessageAndValidation(t *testing.T) {
	raw := json.RawMessage(`{"enabled":true,"start_time_local":"04:15","timezone":"UTC","max_outputs":"3"}`)
	cfg, err := EffectiveDreamingConfig(domain.DreamingRuntimeConfig{Enabled: true}, map[string]any{"dreaming": raw})
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, "03:00", cfg.StartTimeLocal)
	require.Equal(t, 5, cfg.MaxOutputs)
	require.Equal(t, "team", cfg.Source)

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
		"manual": true,
		"max_outputs": 9
	}`), &req))

	require.True(t, req.Manual)
	require.Equal(t, 9, req.MaxOutputs)
}
