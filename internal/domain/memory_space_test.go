package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyPrivateKindsAreRejected(t *testing.T) {
	require.False(t, MemorySpaceKind("profile_private").Valid())
	require.False(t, MemorySpaceKind("credential_private").Valid())
}

func TestMemorySpaceKindsAndLifecycleStatesAreClosed(t *testing.T) {
	for _, kind := range []MemorySpaceKind{
		MemorySpaceTeamShared,
		MemorySpacePrivate,
	} {
		require.True(t, kind.Valid())
	}
	require.False(t, MemorySpaceKind("unknown").Valid())

	for _, state := range []MemorySpaceLifecycleState{
		MemorySpaceActive,
		MemorySpaceSealed,
		MemorySpaceRetired,
	} {
		require.True(t, state.Valid())
	}
	require.False(t, MemorySpaceLifecycleState("unknown").Valid())
}
