package repository

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrapPrivateMemoryErrorBoundsUnexpectedDetails(t *testing.T) {
	raw := errors.New("pq: password=secret connection details")

	err := wrapPrivateMemoryError("list operations", raw)

	require.ErrorIs(t, err, ErrPrivateMemoryInternal)
	require.NotContains(t, err.Error(), raw.Error())
}

func TestWrapPrivateMemoryErrorPreservesKnownSentinels(t *testing.T) {
	err := wrapPrivateMemoryError("claim erasure", ErrPrivateMemoryClaimLost)

	require.ErrorIs(t, err, ErrPrivateMemoryClaimLost)
}
