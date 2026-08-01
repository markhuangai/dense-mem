package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOValidateAPIKeyPrincipalRequiresDurableDirectoryEntitlement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	profileID := uuid.New()
	provider := &domain.SSOProvider{ID: providerID, Enabled: true}
	key := func(identity *uuid.UUID) *domain.APIKey {
		return &domain.APIKey{
			ID:            profileID,
			TeamID:        teamID,
			SSOProviderID: &providerID,
			SSOIdentityID: identity,
			SSOSubject:    "entra-user-1",
			SSOGroupID:    "entra-research-manager",
		}
	}

	repo := &ssoRepositoryStub{
		t:                        t,
		directoryAuthorityActive: true,
		providers:                map[uuid.UUID]*domain.SSOProvider{providerID: provider},
	}
	svc := NewSSOService(repo, SSOConfig{})
	_, err := svc.ValidateAPIKeyPrincipal(ctx, key(nil))
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	repo.directoryProfileEntitled = map[uuid.UUID]bool{profileID: false}
	_, err = svc.ValidateAPIKeyPrincipal(ctx, key(&identityID))
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	repo.directoryProfileEntitled[profileID] = true
	validated, err := svc.ValidateAPIKeyPrincipal(ctx, key(&identityID))
	require.NoError(t, err)
	require.Equal(t, "active", validated.SSOEntitlementStatus)
	require.Equal(t, []uuid.UUID{profileID, profileID}, repo.directoryProfileEntitledCalls)

	backendErr := errors.New("directory membership lookup failed")
	repo.directoryProfileEntitledErr = backendErr
	_, err = svc.ValidateAPIKeyPrincipal(ctx, key(&identityID))
	require.ErrorIs(t, err, backendErr)

	repo.directoryProfileEntitledErr = nil
	repo.directoryAuthorityErr = backendErr
	_, err = svc.ValidateAPIKeyPrincipal(ctx, key(&identityID))
	require.ErrorIs(t, err, backendErr)
}
