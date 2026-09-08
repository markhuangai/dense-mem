package postgres_test

import (
	"errors"
	"testing"

	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/stretchr/testify/require"
)

func TestWrapPrivateMemoryErrorBoundsUnexpectedDetails(t *testing.T) {
	raw := errors.New("pq: password=secret connection details")

	err := privacypostgres.WrapError("list operations", raw)

	require.ErrorIs(t, err, privacypostgres.ErrPrivateMemoryInternal)
	require.NotContains(t, err.Error(), raw.Error())
}

func TestWrapPrivateMemoryErrorPreservesKnownSentinels(t *testing.T) {
	err := privacypostgres.WrapError("claim erasure", privacypostgres.ErrPrivateMemoryClaimLost)

	require.ErrorIs(t, err, privacypostgres.ErrPrivateMemoryClaimLost)
}
