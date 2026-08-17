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

func TestCredentialServiceSSOOwnedMutationsRequireOwner(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	otherOwnerID := uuid.New()
	credentialID := uuid.New()

	mockRepo := new(MockCredentialRepository)
	svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
	mockRepo.On("GetSSOOwnedCredentialByID", ctx, teamID, otherOwnerID, credentialID).Return(nil, nil).Once()

	rotated, rawKey, err := svc.RotateSSOOwnedCredential(ctx, teamID, otherOwnerID, credentialID, CreateCredentialRequest{}, nil, CredentialRoleMember, "", "wrong-owner")
	require.ErrorContains(t, err, "not found")
	require.Nil(t, rotated)
	require.Empty(t, rawKey)
	mockRepo.AssertNotCalled(t, "GetByIDForTeam")
	mockRepo.AssertNotCalled(t, "RotateForTeam")

	mockRepo.On("GetSSOOwnedCredentialByID", ctx, teamID, otherOwnerID, credentialID).Return(nil, nil).Once()
	err = svc.RevokeSSOOwnedCredential(ctx, teamID, otherOwnerID, credentialID, nil, CredentialRoleMember, "", "wrong-owner")
	require.ErrorContains(t, err, "not found")
	mockRepo.AssertNotCalled(t, "RevokeForTeam")
	mockRepo.AssertExpectations(t)
}

func TestCredentialServiceSSOOwnedCollection(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	identityID := uuid.New()
	first := &domain.Credential{ID: uuid.New(), TeamID: teamID, OwnerIdentityID: &identityID}
	second := &domain.Credential{ID: uuid.New(), TeamID: teamID, OwnerIdentityID: &identityID}

	mockRepo := new(MockCredentialRepository)
	svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)
	mockRepo.On("ListSSOOwnedCredentials", ctx, teamID, identityID).Return([]*domain.Credential{first, second}, nil).Once()
	owned, err := svc.ListSSOOwnedCredentials(ctx, teamID, identityID)
	require.NoError(t, err)
	require.Equal(t, []*domain.Credential{first, second}, owned)

	mockRepo.On("GetSSOOwnedCredentialByID", ctx, teamID, identityID, second.ID).Return(second, nil).Once()
	got, err := svc.GetSSOOwnedCredentialByID(ctx, teamID, identityID, second.ID)
	require.NoError(t, err)
	require.Same(t, second, got)
	mockRepo.AssertExpectations(t)
}

func TestCredentialServiceSSOOwnedCollectionErrors(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	identityID := uuid.New()
	credentialID := uuid.New()
	mockRepo := new(MockCredentialRepository)
	svc := NewCredentialService(mockRepo, new(MockTeamService), new(MockAuditService), nil)

	mockRepo.On("ListSSOOwnedCredentials", ctx, teamID, identityID).Return(nil, errors.New("list failed")).Once()
	owned, err := svc.ListSSOOwnedCredentials(ctx, teamID, identityID)
	require.ErrorContains(t, err, "failed to list sso-owned api credentials")
	require.Nil(t, owned)

	mockRepo.On("GetSSOOwnedCredentialByID", ctx, teamID, identityID, credentialID).Return(nil, errors.New("get failed")).Once()
	got, err := svc.GetSSOOwnedCredentialByID(ctx, teamID, identityID, credentialID)
	require.ErrorContains(t, err, "failed to get sso-owned api credential")
	require.Nil(t, got)
	mockRepo.AssertExpectations(t)
}
