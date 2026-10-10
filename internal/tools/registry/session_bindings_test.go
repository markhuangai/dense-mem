package registry

import (
	"context"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	service "github.com/markhuangai/dense-mem/internal/session/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSessionPrivateScopeRejectsMissingSharedAndReadOnlyActors(t *testing.T) {
	team, owner, space := uuid.New(), uuid.New(), uuid.New()
	for _, test := range []struct {
		kind    domain.MemorySpaceKind
		grants  []string
		allowed bool
	}{
		{domain.MemorySpaceProfilePrivate, []string{"read", "write"}, true},
		{domain.MemorySpaceCredentialPrivate, []string{"write"}, true},
		{domain.MemorySpaceTeamShared, []string{"read", "write"}, false},
		{domain.MemorySpaceProfilePrivate, []string{"read"}, false},
	} {
		ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: team, OwnerID: owner, Grants: test.grants, AllowedSpaces: []domain.MemorySpaceAccess{{ID: space, Kind: test.kind, Generation: 1}}})
		scope, err := service.PrivateScope(ctx)
		if test.allowed {
			require.NoError(t, err)
			require.Equal(t, space.String(), scope.SpaceID)
		} else {
			require.ErrorIs(t, err, session.ErrUnauthorized)
		}
	}
	_, err := service.PrivateScope(context.Background())
	require.ErrorIs(t, err, session.ErrUnauthorized)
}
func TestSessionDiscoveryAndInvocationUseSameUnavailablePredicate(t *testing.T) {
	api := service.NewService(service.Dependencies{Enabled: false})
	tool := bindSessionTool(Tool{Name: ToolIngestSession, InputSchema: sessionInputSchema(), OutputSchema: sessionOutputSchema()}, Dependencies{SessionBindings: SessionBindings{Service: api}})
	require.False(t, ToolVisible(context.Background(), tool, RuntimeToolPolicy{}))
	_, err := tool.Invoke(context.Background(), "ignored", map[string]any{})
	require.ErrorIs(t, err, ErrToolDisabled)
	require.True(t, ContractToolRuntimeOptional(ToolIngestSession))
}
