package access

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestDirectoryPageValidationPreservesServiceErrorBoundary(t *testing.T) {
	t.Parallel()
	// Any storage call panics, so every rejection must come from the real service policy.
	svc := NewDirectoryIdentityService(struct{ DirectoryIdentityStore }{}, DirectoryIdentityConfig{})
	for _, request := range []domain.DirectoryPageRequest{
		{Offset: -1}, {Limit: -1}, {Limit: 101},
		{FilterField: "email"}, {FilterField: "id", FilterValue: "not-a-uuid"},
	} {
		_, _, err := svc.ListUsersPage(context.Background(), uuid.New(), request)
		require.ErrorIs(t, err, ErrDirectoryInvalidValue)
		require.NotErrorIs(t, err, accesscontract.ErrDirectoryInvalidValue)
		_, _, err = svc.ListGroupsPage(context.Background(), uuid.New(), request)
		require.ErrorIs(t, err, ErrDirectoryInvalidValue)
		require.NotErrorIs(t, err, accesscontract.ErrDirectoryInvalidValue)
	}
}
