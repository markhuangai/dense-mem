package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCoordinationHelperRequiresCallerTransaction(t *testing.T) {
	ctx := context.Background()
	require.Error(t, EnsureActiveTeamForMutation(ctx, nil, "team"))
}

func TestCoordinationHelperDiscardsNilConnection(t *testing.T) {
	require.NoError(t, DiscardAdvisoryLockConnection(nil))
}
