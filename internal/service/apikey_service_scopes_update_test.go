package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAPIKeyServiceUpdateScopesForProfile(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("member profile updates scopes", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, nil, nil)
		key := &domain.APIKey{
			ID:        keyID,
			ProfileID: profileID,
			TeamID:    profileID,
			Name:      "ops",
			Label:     "ops",
			KeyHash:   "secret",
			Scopes:    []string{APIKeyScopeRead, APIKeyScopeWrite},
			Role:      APIKeyRoleMember,
		}
		wantScopes := []string{APIKeyScopeRead, APIKeyScopeFeedbackRead}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateScopesForProfile", ctx, profileID, keyID, wantScopes).Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
			scopes, ok := entry.AfterPayload["scopes"].([]string)
			return entry.Operation == "UPDATE" &&
				entry.EntityType == "team_profile" &&
				entry.EntityID == keyID.String() &&
				ok &&
				assert.ObjectsAreEqual(wantScopes, scopes)
		})).Return(nil).Once()

		updated, err := svc.UpdateScopesForProfile(ctx, profileID, keyID, []string{"feedback:read", "READ"}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, wantScopes, updated.Scopes)
		assert.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("manager profile keeps write scope while toggling feedback", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, nil, nil)
		key := &domain.APIKey{
			ID:        keyID,
			ProfileID: profileID,
			TeamID:    profileID,
			Name:      "manager",
			KeyHash:   "secret",
			Scopes:    []string{APIKeyScopeRead, APIKeyScopeWrite},
			Role:      APIKeyRoleManager,
		}
		wantScopes := []string{APIKeyScopeRead, APIKeyScopeWrite, APIKeyScopeFeedbackRead}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateScopesForProfile", ctx, profileID, keyID, wantScopes).Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(nil).Once()

		updated, err := svc.UpdateScopesForProfile(ctx, profileID, keyID, []string{APIKeyScopeRead, APIKeyScopeFeedbackRead}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, wantScopes, updated.Scopes)
		assert.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("unchanged scopes skip repository update", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, nil, nil)
		key := &domain.APIKey{
			ID:        keyID,
			ProfileID: profileID,
			TeamID:    profileID,
			Name:      "reader",
			KeyHash:   "secret",
			Scopes:    []string{APIKeyScopeRead},
			Role:      APIKeyRoleMember,
		}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()

		updated, err := svc.UpdateScopesForProfile(ctx, profileID, keyID, []string{APIKeyScopeRead}, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		assert.Equal(t, []string{APIKeyScopeRead}, updated.Scopes)
		assert.Empty(t, updated.KeyHash)
		mockRepo.AssertExpectations(t)
		mockRepo.AssertNotCalled(t, "UpdateScopesForProfile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		mockAuditService.AssertNotCalled(t, "Append", mock.Anything, mock.Anything)
	})

	t.Run("invalid scopes fail before lookup", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)

		updated, err := svc.UpdateScopesForProfile(ctx, profileID, keyID, []string{APIKeyScopeWrite}, nil, "system", "127.0.0.1", "corr")

		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertNotCalled(t, "GetByIDForProfile", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("empty scopes fail before lookup", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)

		updated, err := svc.UpdateScopesForProfile(ctx, profileID, keyID, []string{}, nil, "system", "127.0.0.1", "corr")

		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertNotCalled(t, "GetByIDForProfile", mock.Anything, mock.Anything, mock.Anything)
		mockRepo.AssertNotCalled(t, "UpdateScopesForProfile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}
