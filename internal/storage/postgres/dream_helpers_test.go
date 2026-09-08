package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDreamHelperRequiresCallerTransaction(t *testing.T) {
	ctx := context.Background()
	require.Error(t, SeedTeamPredicateDefinitions(ctx, nil, "team"))
	require.Error(t, EnsureActiveTeamForMutation(ctx, nil, "team"))
	_, err := LoadPredicateDefinition(ctx, nil, "team", "uses", 1)
	require.Error(t, err)
}

func TestDreamHelperDiscardsNilConnection(t *testing.T) {
	require.NoError(t, DiscardAdvisoryLockConnection(nil))
}
