package access

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestCredentialServiceUpdateScopesForTeam(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("member profile updates scopes", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, nil)
		key := &domain.Credential{
			ID:      keyID,
			TeamID:  profileID,
			Name:    "ops",
			KeyHash: "secret",
			Scopes:  []string{CredentialScopeRead, CredentialScopeWrite},
			Role:    CredentialRoleMember,
		}
		wantScopes := []string{CredentialScopeRead, CredentialScopeFeedbackRead}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateScopesForTeam", ctx, profileID, keyID, wantScopes).Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
			scopes, ok := entry.AfterPayload["scopes"].([]string)
			return entry.Operation == "UPDATE" &&
				entry.EntityType == "api_key" &&
				entry.EntityID == keyID.String() &&
				ok &&
				assert.ObjectsAreEqual(wantScopes, scopes)
		})).Return(nil).Once()

		updated, err := svc.UpdateScopesForTeam(ctx, profileID, keyID, []string{"feedback:read", "READ"}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, wantScopes, updated.Scopes)
		assert.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("manager profile keeps write scope while toggling feedback", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, nil)
		key := &domain.Credential{
			ID:      keyID,
			TeamID:  profileID,
			Name:    "manager",
			KeyHash: "secret",
			Scopes:  []string{CredentialScopeRead, CredentialScopeWrite},
			Role:    CredentialRoleManager,
		}
		wantScopes := []string{CredentialScopeRead, CredentialScopeWrite, CredentialScopeFeedbackRead}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateScopesForTeam", ctx, profileID, keyID, wantScopes).Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.AnythingOfType("access.AuditLogEntry")).Return(nil).Once()

		updated, err := svc.UpdateScopesForTeam(ctx, profileID, keyID, []string{CredentialScopeRead, CredentialScopeFeedbackRead}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, wantScopes, updated.Scopes)
		assert.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("unchanged scopes skip repository update", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, nil)
		key := &domain.Credential{
			ID:      keyID,
			TeamID:  profileID,
			Name:    "reader",
			KeyHash: "secret",
			Scopes:  []string{CredentialScopeRead},
			Role:    CredentialRoleMember,
		}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()

		updated, err := svc.UpdateScopesForTeam(ctx, profileID, keyID, []string{CredentialScopeRead}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, []string{CredentialScopeRead}, updated.Scopes)
		assert.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
		mockRepo.AssertNotCalled(t, "UpdateScopesForTeam", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockAuditService.AssertNotCalled(t, "Append", mock.Anything, mock.Anything)
	})

	t.Run("invalid scopes fail before lookup", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)

		updated, err := svc.UpdateScopesForTeam(ctx, profileID, keyID, []string{CredentialScopeWrite}, nil, "system", "127.0.0.1", "corr")

		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertNotCalled(t, "GetByIDForTeam", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("empty scopes fail before lookup", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)

		updated, err := svc.UpdateScopesForTeam(ctx, profileID, keyID, []string{}, nil, "system", "127.0.0.1", "corr")

		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertNotCalled(t, "GetByIDForTeam", mock.Anything, mock.Anything, mock.Anything)
		mockRepo.AssertNotCalled(t, "UpdateScopesForTeam", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}
