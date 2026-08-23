package access

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestCredentialServiceUpdateRoleBranches(t *testing.T) {
	ctx := context.Background()
	profileID := uuid.New()
	keyID := uuid.New()

	t.Run("normalizes valid roles and rejects invalid role", func(t *testing.T) {
		role, err := NormalizeCredentialRole(" Manager ")
		require.NoError(t, err)
		require.Equal(t, CredentialRoleManager, role)

		role, err = NormalizeCredentialRole("member")
		require.NoError(t, err)
		require.Equal(t, CredentialRoleMember, role)

		role, err = NormalizeCredentialRole(" ")
		require.NoError(t, err)
		require.Empty(t, role)

		role, err = NormalizeCredentialRole("maintainer")
		require.Error(t, err)
		require.Empty(t, role)

		role, err = NormalizeCredentialRole("owner")
		require.Error(t, err)
		require.Empty(t, role)
	})

	t.Run("requires role", func(t *testing.T) {
		svc := NewCredentialService(new(MockCredentialRepository), new(MockTeamService), new(MockAuditService), nil)
		updated, err := svc.UpdateRoleForTeam(ctx, profileID, keyID, " ", nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
	})

	t.Run("same role returns sanitized key without update", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "research", Role: CredentialRoleMember, KeyHash: "secret"}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)

		updated, err := svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleMember, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		require.Empty(t, updated.KeyHash)
		require.Equal(t, CredentialRoleMember, updated.Role)
		mockRepo.AssertExpectations(t)
	})

	t.Run("promotes read-only member to manager with write scope", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, nil)
		key := &domain.Credential{
			ID:      keyID,
			TeamID:  profileID,
			Name:    "research",
			Role:    CredentialRoleMember,
			Scopes:  []string{CredentialScopeRead, CredentialScopeFeedbackRead},
			KeyHash: "secret",
		}
		wantScopes := []string{CredentialScopeRead, CredentialScopeWrite, CredentialScopeFeedbackRead}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil)
		mockRepo.On("UpdateRoleForTeam", ctx, profileID, keyID, CredentialRoleManager, wantScopes).Return(int64(1), nil)
		mockAuditService.On("Append", ctx, mock.MatchedBy(func(entry AuditLogEntry) bool {
			return entry.Operation == "UPDATE" &&
				entry.EntityType == "api_key" &&
				entry.EntityID == keyID.String() &&
				entry.AfterPayload["role"] == CredentialRoleManager &&
				reflect.DeepEqual(entry.AfterPayload["scopes"], wantScopes)
		})).Return(nil)

		updated, err := svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleManager, nil, "system", "127.0.0.1", "corr")

		require.NoError(t, err)
		require.NotNil(t, updated)
		require.Empty(t, updated.KeyHash)
		require.Equal(t, CredentialRoleManager, updated.Role)
		require.Equal(t, wantScopes, updated.Scopes)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})

	t.Run("not found and rows affected zero", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, nil).Once()
		updated, err := svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleManager, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)

		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "research", Role: CredentialRoleMember}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateRoleForTeam", ctx, profileID, keyID, CredentialRoleManager, StandardCredentialScopes()).Return(int64(0), nil).Once()
		updated, err = svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleManager, nil, "system", "127.0.0.1", "corr")
		require.Error(t, err)
		require.Nil(t, updated)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository and audit errors", func(t *testing.T) {
		mockRepo := new(MockCredentialRepository)
		mockAuditService := new(MockAuditService)
		svc := NewCredentialService(mockRepo, new(MockTeamService), mockAuditService, nil)

		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(nil, errors.New("get failed")).Once()
		updated, err := svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleManager, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to get credential")
		require.Nil(t, updated)

		key := &domain.Credential{ID: keyID, TeamID: profileID, Name: "research", Role: CredentialRoleMember}
		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateRoleForTeam", ctx, profileID, keyID, CredentialRoleManager, StandardCredentialScopes()).Return(int64(0), errors.New("update failed")).Once()
		updated, err = svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleManager, nil, "system", "127.0.0.1", "corr")
		require.ErrorContains(t, err, "failed to update credential role")
		require.Nil(t, updated)

		mockRepo.On("GetByIDForTeam", ctx, profileID, keyID).Return(key, nil).Once()
		mockRepo.On("UpdateRoleForTeam", ctx, profileID, keyID, CredentialRoleManager, StandardCredentialScopes()).Return(int64(1), nil).Once()
		mockAuditService.On("Append", ctx, mock.AnythingOfType("access.AuditLogEntry")).Return(errors.New("audit failed")).Once()
		updated, err = svc.UpdateRoleForTeam(ctx, profileID, keyID, CredentialRoleManager, nil, "system", "127.0.0.1", "corr")
		require.NoError(t, err)
		require.Equal(t, CredentialRoleManager, updated.Role)
		mockRepo.AssertExpectations(t)
		mockAuditService.AssertExpectations(t)
	})
}
