package ontology

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestPreparationCooldownFencesNewAdmissionAndRestoresRepeated429(t *testing.T) {
	path := filepath.Join(t.TempDir(), "until")
	cooldown, err := RestorePreparationCooldown(path)
	require.NoError(t, err)
	now := time.Now().UTC()
	cooldown.now = func() time.Time { return now }
	var admitted int
	admit := func() error {
		return cooldown.admit(func() error { admitted++; return nil })
	}
	ctx := modelprovider.WithRateLimitObserver(context.Background(), func(retryAt time.Time) {
		cooldown.hold(context.Background(), nil, retryAt)
	})
	require.NoError(t, admit())
	modelprovider.ObserveRateLimit(ctx, "", now)
	require.ErrorIs(t, admit(), contract.ErrMaintenancePaused)
	now = now.Add(59 * time.Second)
	modelprovider.ObserveRateLimit(ctx, "3600", now)
	now = now.Add(16 * time.Minute)
	require.ErrorIs(t, admit(), contract.ErrMaintenancePaused)
	restored, err := RestorePreparationCooldown(path)
	require.NoError(t, err)
	restored.now = cooldown.now
	require.ErrorIs(t, restored.check(), contract.ErrMaintenancePaused)
	require.Equal(t, 1, admitted, "a long cooldown consumes no new admission or lease")
	now = now.Add(44 * time.Minute)
	require.NoError(t, restored.check())
	require.NoError(t, admit())
	require.Equal(t, 2, admitted)
}
