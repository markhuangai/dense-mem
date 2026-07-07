package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAPIKeyServiceUpdateRoleBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("normalizes valid roles and rejects invalid role", func(t *testing.T) {
		role, err := NormalizeAPIKeyRole(" Manager ")
		require.NoError(t, err)
		require.Equal(t, APIKeyRoleManager, role)

		role, err = NormalizeAPIKeyRole("member")
		require.NoError(t, err)
		require.Equal(t, APIKeyRoleMember, role)

		role, err = NormalizeAPIKeyRole(" ")
		require.NoError(t, err)
		require.Empty(t, role)

		role, err = NormalizeAPIKeyRole("maintainer")
		require.Error(t, err)
		require.Empty(t, role)

		role, err = NormalizeAPIKeyRole("owner")
		require.Error(t, err)
		require.Empty(t, role)
	})

	t.Run("requires role", func(t *testing.T) {
		svc := NewAPIKeyService(new(MockAPIKeyRepository), new(MockProfileService), new(MockAuditService), nil, nil)
		updated, err := svc.UpdateRoleForProfile(ctx, profileID, keyID, " ", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
	})

	t.Run("same role returns sanitized key without update", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Role: APIKeyRoleMember, KeyHash: "secret"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)

		updated, err := svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleMember, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		require.Empty(t, updated.KeyHash)
		require.Equal(t, APIKeyRoleMember, updated.Role)
		mockRepo.AssertExpectations(t)
	})

	t.Run("updates role and writes audit", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, nil, nil)
		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Role: APIKeyRoleMember, KeyHash: "secret"}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil)
		mockRepo.On("UpdateRoleForProfile", ctx, profileID, keyID, APIKeyRoleManager).Return(int64(1), nil)
		mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
			return entry.Operation == "UPDATE" &&
				entry.EntityType == "team_profile" &&
				entry.EntityID == keyID.String() &&
				entry.AfterPayload["role"] == APIKeyRoleManager
		})).Return(nil)

		updated, err := svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleManager, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		require.Empty(t, updated.KeyHash)
		require.Equal(t, APIKeyRoleManager, updated.Role)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("not found and rows affected zero", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), new(MockAuditService), nil, nil)
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, nil).Once()
		updated, err := svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleManager, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)

		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Role: APIKeyRoleMember}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateRoleForProfile", ctx, profileID, keyID, APIKeyRoleManager).Return(int64(0), nil).Once()
		updated, err = svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleManager, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository and audit errors", func(t *testing.T) {
		mockRepo := new(MockAPIKeyRepository)
		mockAuditService := new(MockAuditService)
		svc := NewAPIKeyService(mockRepo, new(MockProfileService), mockAuditService, nil, nil)

		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(nil, errors.New("get failed")).Once()
		updated, err := svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleManager, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to get team profile")
		require.Nil(t, updated)

		key := &domain.APIKey{ID: keyID, ProfileID: profileID, TeamID: profileID, Name: "research", Role: APIKeyRoleMember}
		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateRoleForProfile", ctx, profileID, keyID, APIKeyRoleManager).Return(int64(0), errors.New("update failed")).Once()
		updated, err = svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleManager, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to update team profile role")
		require.Nil(t, updated)

		mockRepo.On("GetByIDForProfile", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateRoleForProfile", ctx, profileID, keyID, APIKeyRoleManager).Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.AnythingOfType("service.AuditLogEntry")).Return(errors.New("audit failed")).Once()
		updated, err = svc.UpdateRoleForProfile(ctx, profileID, keyID, APIKeyRoleManager, nil, "system", "127.0.0.1", "corr")
		require.NoError(t, err)
		require.Equal(t, APIKeyRoleManager, updated.Role)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})
}
