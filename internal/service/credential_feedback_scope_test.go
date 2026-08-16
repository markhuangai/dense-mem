package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestNormalizeCredentialScopesAcceptsFeedbackReadScope(t *testing.T) {
	normalized, err := NormalizeCredentialScopes([]string{"feedback:read", "Read"})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "feedback:read"}, normalized)

	normalized, err = NormalizeCredentialScopes([]string{"feedback:read", "write", "Read"})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write", "feedback:read"}, normalized)
}

func TestCreateCredentialManagerPreservesExplicitFeedbackReadScope(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	mockRepo := new(MockCredentialRepository)
	mockTeamService := new(MockTeamService)
	mockAuditService := new(MockAuditService)
	svc := NewCredentialService(mockRepo, mockTeamService, mockAuditService, nil)
	mockTeamService.On("Get", ctx, profileID).Return(&domain.Team{ID: profileID}, nil)
	mockRepo.On("CreateCredential", ctx, mock.AnythingOfType("*domain.Credential")).Run(func(args mock.Arguments) {
		key := args.Get(1).(*domain.Credential)
		key.ID = uuid.New()
		require.Equal(t, []string{CredentialScopeRead, CredentialScopeWrite, CredentialScopeFeedbackRead}, key.Scopes)
		require.Equal(t, CredentialRoleManager, key.Role)
	}).Return(nil)
	mockAuditService.On("CredentialCreated", ctx, mock.AnythingOfType("*string"), mock.AnythingOfType("string"), mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	key, raw, err := svc.CreateCredential(ctx, profileID, CreateCredentialRequest{Name: "manager", Scopes: []string{CredentialScopeRead, CredentialScopeFeedbackRead}, Role: CredentialRoleManager}, nil, "system", "127.0.0.1", "corr")

	require.NoError(t, err)
	require.NotNil(t, key)
	require.NotEmpty(t, raw)
	mockRepo.AssertExpectations(t)
	mockTeamService.AssertExpectations(t)
	mockAuditService.AssertExpectations(t)
}
