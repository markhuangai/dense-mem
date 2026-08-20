package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialMemoryBindingValidationAndSpaceKind(t *testing.T) {
	for _, tc := range []struct {
		binding CredentialMemoryBinding
		kind    MemorySpaceKind
	}{
		{CredentialBindingSharedOnly, MemorySpaceTeamShared},
		{CredentialBindingProfilePrivate, MemorySpaceProfilePrivate},
		{CredentialBindingCredentialPrivate, MemorySpaceCredentialPrivate},
	} {
		require.True(t, tc.binding.Valid())
		require.Equal(t, tc.kind, tc.binding.SpaceKind())
	}
	_, err := NormalizeCredentialMemoryBinding("unknown")
	require.Error(t, err)
	normalized, err := NormalizeCredentialMemoryBinding("")
	require.NoError(t, err)
	require.Equal(t, CredentialBindingSharedOnly, normalized)
	normalized, err = NormalizeCredentialMemoryBinding(string(CredentialBindingProfilePrivate))
	require.NoError(t, err)
	require.Equal(t, CredentialBindingProfilePrivate, normalized)
	require.False(t, CredentialMemoryBinding("unknown").Valid())
}

func TestMemorySpaceKindsAndLifecycleStatesAreClosed(t *testing.T) {
	for _, kind := range []MemorySpaceKind{
		MemorySpaceTeamShared,
		MemorySpaceProfilePrivate,
		MemorySpaceCredentialPrivate,
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
