package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestCredentialServiceGetSSOOwnedCredential(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	identityID := uuid.New()
	key := &domain.Credential{
		ID:              uuid.New(),
		TeamID:          profileID,
		Name:            "Owned",
		OwnerIdentityID: &identityID,
	}

	mockRepo := new(MockCredentialRepository)
	svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
	mockRepo.On("GetSSOOwnedCredential", ctx, profileID, identityID).Return(key, nil).Once()
	got, err := svc.GetSSOOwnedCredential(ctx, profileID, identityID)
	require.NoError(t, err)
	require.Equal(t, key, got)

	repoErr := errors.New("lookup failed")
	mockRepo.On("GetSSOOwnedCredential", ctx, profileID, identityID).Return(nil, repoErr).Once()
	got, err = svc.GetSSOOwnedCredential(ctx, profileID, identityID)
	require.ErrorContains(t, err, "failed to get sso-owned api credential for team")
	require.Nil(t, got)
	mockRepo.AssertExpectations(t)
}
