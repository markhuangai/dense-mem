package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDirectoryIdentityServiceRejectsOperationsWhenUnavailable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var unavailable *DirectoryIdentityService
	connectorID := uuid.New()

	_, err := unavailable.GetConnector(ctx, connectorID)
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.GetConnectorForProvider(ctx, connectorID)
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.ListConnectors(ctx)
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.GetUser(ctx, connectorID, uuid.New())
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.ListUsers(ctx, connectorID)
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.GetGroup(ctx, connectorID, uuid.New())
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.ListGroups(ctx, connectorID)
	require.ErrorContains(t, err, "unavailable")
	_, err = unavailable.Preview(ctx, connectorID)
	require.ErrorContains(t, err, "unavailable")
	_, _, err = unavailable.IssueOAuthToken(ctx, "client", "secret")
	require.ErrorContains(t, err, "unavailable")
}
