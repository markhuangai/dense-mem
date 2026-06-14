package http

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEffectiveDreamingConfigHelper(t *testing.T) {
	ctx := context.Background()

	effective := effectiveDreamingConfig(ctx, &controlAppConfigSvc{
		dreamingRuntime: domain.DreamingRuntimeConfig{
			Enabled:           true,
			ForceEnabled:      true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: true,
			DreamEnabled:      true,
			MaxOutputs:        5,
		},
	}, map[string]any{"dreaming": map[string]any{"enabled": false}})
	require.NotNil(t, effective)
	require.True(t, effective.Enabled)
	require.True(t, effective.TeamEnabled)
	require.Equal(t, "global_force", effective.Source)

	effective = effectiveDreamingConfig(ctx, nil, map[string]any{"dreaming": map[string]any{"enabled": true}})
	require.NotNil(t, effective)
	require.True(t, effective.Enabled)
	require.Equal(t, "team", effective.Source)

	require.Nil(t, effectiveDreamingConfig(ctx, &controlAppConfigSvc{
		dreamingRuntimeErr: errors.New("config failed"),
	}, nil))
	require.Nil(t, effectiveDreamingConfig(ctx, &controlAppConfigSvc{
		dreamingRuntime: domain.DreamingRuntimeConfig{
			StartTimeLocal: "99:99",
			Timezone:       "UTC",
			MaxOutputs:     5,
		},
	}, nil))
}
