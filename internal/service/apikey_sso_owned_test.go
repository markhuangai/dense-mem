package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAPIKeyServiceGetSSOOwnedKey(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	identityID := uuid.New()
	key := &domain.APIKey{
		ID:                 uuid.New(),
		ProfileID:          profileID,
		TeamID:             profileID,
		Name:               "Owned",
		SSOOwnerIdentityID: &identityID,
	}

	mockRepo := new(MockAPIKeyRepository)
	svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
	mockRepo.On("GetSSOOwnedKey", ctx, profileID, identityID).Return(key, nil).Once()
	got, err := svc.GetSSOOwnedKey(ctx, profileID, identityID)
	require.NoError(t, err)
	require.Equal(t, key, got)

	repoErr := errors.New("lookup failed")
	mockRepo.On("GetSSOOwnedKey", ctx, profileID, identityID).Return(nil, repoErr).Once()
	got, err = svc.GetSSOOwnedKey(ctx, profileID, identityID)
	require.ErrorContains(t, err, "failed to get sso-owned api key for profile")
	require.Nil(t, got)
	mockRepo.AssertExpectations(t)
}
