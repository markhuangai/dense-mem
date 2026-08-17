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
}
