package modelprovider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConcurrencyGateBlocksCanceledWaitersAndReleasesSlots(t *testing.T) {
	gate := NewConcurrencyGate(1)
	require.NoError(t, AcquireConcurrency(context.Background(), gate))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, AcquireConcurrency(ctx, gate), context.Canceled)

	ReleaseConcurrency(gate)
	require.NoError(t, AcquireConcurrency(context.Background(), gate))
	ReleaseConcurrency(gate)
}

func TestConcurrencyGateAllowsNilGate(t *testing.T) {
	require.NoError(t, AcquireConcurrency(context.Background(), nil))
	ReleaseConcurrency(nil)
}

func TestConcurrencyGateNormalizesNonPositiveLimits(t *testing.T) {
	gate := NewConcurrencyGate(0)
	require.NoError(t, AcquireConcurrency(context.Background(), gate))
	ReleaseConcurrency(gate)
}
