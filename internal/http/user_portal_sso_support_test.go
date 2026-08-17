package http

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (r *userPortalAuthRepo) ListSSOOwnedCredentials(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Credential, error) {
	return nil, nil
}

func (r *userPortalAuthRepo) GetSSOOwnedCredentialByID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}

func (s *userPortalKeySvc) RotateSSOOwnedCredential(ctx context.Context, teamID, identityID, credentialID uuid.UUID, req service.CreateCredentialRequest, actorCredentialID *string, actorRole, clientIP, correlationID string) (*domain.Credential, string, error) {
	key, err := s.GetSSOOwnedCredentialByID(ctx, teamID, identityID, credentialID)
	if err != nil {
		return nil, "", err
	}
	return s.RotateForTeam(ctx, teamID, key.ID, req, actorCredentialID, actorRole, clientIP, correlationID)
}

func (s *userPortalKeySvc) ListSSOOwnedCredentials(_ context.Context, profileID, identityID uuid.UUID) ([]*domain.Credential, error) {
	out := make([]*domain.Credential, 0)
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.OwnerIdentityID != nil && *key.OwnerIdentityID == identityID && key.RevokedAt == nil {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *userPortalKeySvc) GetSSOOwnedCredentialByID(_ context.Context, profileID, identityID, credentialID uuid.UUID) (*domain.Credential, error) {
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.ID == credentialID && key.OwnerIdentityID != nil && *key.OwnerIdentityID == identityID && key.RevokedAt == nil {
			return key, nil
		}
	}
	return nil, httperr.New(httperr.NOT_FOUND, "key not found")
}

func (s *userPortalKeySvc) RevokeForTeam(_ context.Context, profileID, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.ID == id && key.RevokedAt == nil {
			now := time.Now().UTC()
			key.RevokedAt = &now
			return nil
		}
	}
	return httperr.New(httperr.NOT_FOUND, "key not found")
}

func (s *userPortalKeySvc) RevokeSSOOwnedCredential(ctx context.Context, teamID, identityID, credentialID uuid.UUID, actorCredentialID *string, actorRole, clientIP, correlationID string) error {
	key, err := s.GetSSOOwnedCredentialByID(ctx, teamID, identityID, credentialID)
	if err != nil {
		return err
	}
	return s.RevokeForTeam(ctx, teamID, key.ID, actorCredentialID, actorRole, clientIP, correlationID)
}
