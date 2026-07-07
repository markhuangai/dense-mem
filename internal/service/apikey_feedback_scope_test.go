package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestNormalizeAPIKeyScopesAcceptsFeedbackReadScope(t *testing.T) {
	normalized, err := NormalizeAPIKeyScopes([]string{"feedback:read", "Read"})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "feedback:read"}, normalized)

	normalized, err = NormalizeAPIKeyScopes([]string{"feedback:read", "write", "Read"})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write", "feedback:read"}, normalized)
}

func TestCreateStandardKeyManagerPreservesExplicitFeedbackReadScope(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	mockRepo := new(MockAPIKeyRepository)
	mockProfileService := new(MockProfileService)
	mockAuditService := new(MockAuditService)
	svc := NewAPIKeyService(mockRepo, mockProfileService, mockAuditService, nil, nil)
	mockProfileService.On("Get", ctx, profileID).Return(&domain.Profile{ID: profileID}, nil)
	mockRepo.On("CreateStandardKey", ctx, mock.AnythingOfType("*domain.APIKey")).Run(func(args mock.Arguments) {
		key := args.Get(1).(*domain.APIKey)
		key.ID = uuid.New()
		require.Equal(t, []string{APIKeyScopeRead, APIKeyScopeWrite, APIKeyScopeFeedbackRead}, key.Scopes)
		require.Equal(t, APIKeyRoleManager, key.Role)
	}).Return(nil)
	mockAuditService.On("APIKeyCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	key, raw, err := svc.CreateStandardKey(ctx, profileID, CreateAPIKeyRequest{Name: "manager", Scopes: []string{APIKeyScopeRead, APIKeyScopeFeedbackRead}, Role: APIKeyRoleManager}, nil, "system", "127.0.0.1", "corr")

	require.NoError(t, err)
	require.NotNil(t, key)
	require.NotEmpty(t, raw)
	mockRepo.AssertExpectations(t)
	mockProfileService.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}
