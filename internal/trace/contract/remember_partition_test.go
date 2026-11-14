package contract

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRememberPartitionTraceBounds(t *testing.T) {
	require.Equal(t, 16384, NormalizeInput(Input{MaxFragmentContentRunes: 16384}).MaxFragmentContentRunes)
	require.Equal(t, 16384, NormalizeInput(Input{MaxFragmentContentRunes: 16385}).MaxFragmentContentRunes)
	require.Equal(t, 2000, NormalizeInput(Input{}).MaxFragmentContentRunes)
}
