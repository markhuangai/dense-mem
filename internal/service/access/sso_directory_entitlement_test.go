package access

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOValidateCredentialRequiresDurableDirectoryEntitlement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	profileID := uuid.New()
	provider := &domain.SSOProvider{ID: providerID, Enabled: true}
	key := func(identity *uuid.UUID) *domain.Credential {
		return &domain.Credential{
			ID:              profileID,
			TeamID:          teamID,
			Scopes:          []string{CredentialScopeRead},
			SSOProviderID:   &providerID,
			OwnerIdentityID: identity,
			SSOSubject:      "entra-user-1",
			SSOGroupID:      "entra-research-manager",
		}
	}

	repo := &ssoRepositoryStub{
		t:                        t,
		directoryAuthorityActive: true,
		providers:                map[uuid.UUID]*domain.SSOProvider{providerID: provider},
	}
	svc := NewSSOService(repo, SSOConfig{})
	validated, err := svc.ValidateCredential(ctx, key(nil))
	require.NoError(t, err)
	require.NotNil(t, validated)

	repo.directoryProfileEntitled = map[uuid.UUID]bool{identityID: false}
	_, err = svc.ValidateCredential(ctx, key(&identityID))
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	repo.directoryProfileEntitled[identityID] = true
	validated, err = svc.ValidateCredential(ctx, key(&identityID))
	require.NoError(t, err)
	require.Equal(t, "active", validated.SSOEntitlementStatus)
	require.Equal(t, []uuid.UUID{identityID, identityID}, repo.directoryProfileEntitledCalls)

	backendErr := errors.New("directory membership lookup failed")
	repo.directoryProfileEntitledErr = backendErr
	_, err = svc.ValidateCredential(ctx, key(&identityID))
	require.ErrorIs(t, err, backendErr)

	repo.directoryProfileEntitledErr = nil
	repo.directoryAuthorityErr = backendErr
	_, err = svc.ValidateCredential(ctx, key(&identityID))
	require.ErrorIs(t, err, backendErr)
}
