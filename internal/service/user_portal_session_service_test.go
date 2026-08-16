package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type portalSessionRepositoryStub struct {
	sessions         map[string]*domain.UserPortalSession
	deletedExpiredAt time.Time
	createErr        error
	getErr           error
	deleteErr        error
	deleteExpiredErr error
}

func (r *portalSessionRepositoryStub) CreateSession(_ context.Context, session *domain.UserPortalSession) error {
	if r.createErr != nil {
		return r.createErr
	}
	if r.sessions == nil {
		r.sessions = make(map[string]*domain.UserPortalSession)
	}
	copy := *session
	r.sessions[session.SessionHash] = &copy
	return nil
}

func (r *portalSessionRepositoryStub) GetSession(_ context.Context, sessionHash string) (*domain.UserPortalSession, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	session := r.sessions[sessionHash]
	if session == nil {
		return nil, nil
	}
	copy := *session
	return &copy, nil
}

func (r *portalSessionRepositoryStub) DeleteSession(_ context.Context, sessionHash string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	delete(r.sessions, sessionHash)
	return nil
}

func (r *portalSessionRepositoryStub) DeleteExpiredSessions(_ context.Context, now time.Time) error {
	if r.deleteExpiredErr != nil {
		return r.deleteExpiredErr
	}
	r.deletedExpiredAt = now
	for hash, session := range r.sessions {
		if !session.ExpiresAt.After(now) {
			delete(r.sessions, hash)
		}
	}
	return nil
}

type activePortalKeyRepositoryStub struct {
	keys map[uuid.UUID]*domain.Credential
	err  error
}

func (r *activePortalKeyRepositoryStub) GetActiveByID(_ context.Context, id uuid.UUID) (*domain.Credential, error) {
	if r.err != nil {
		return nil, r.err
	}
	key := r.keys[id]
	if key == nil {
		return nil, nil
	}
	copy := *key
	copy.Scopes = append([]string{}, key.Scopes...)
	return &copy, nil
}

func TestUserPortalSessionServiceUsesFixedOpaqueSevenDaySessions(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	keyID := uuid.New()
	actorID := uuid.New()
	membershipID := uuid.New()
	ownerID := uuid.New()
	repo := &portalSessionRepositoryStub{sessions: make(map[string]*domain.UserPortalSession)}
	keys := &activePortalKeyRepositoryStub{keys: map[uuid.UUID]*domain.Credential{
		keyID: {ID: keyID, ActorIdentityID: actorID, MembershipID: membershipID, OwnerID: ownerID, TeamID: uuid.New(), Scopes: []string{"read"}},
	}}
	service := NewUserPortalSessionService(repo, keys, func() time.Time { return now })

	created, err := service.CreateSession(context.Background(), keyID)
	require.NoError(t, err)
	require.Equal(t, now.Add(UserPortalSessionTTL), created.ExpiresAt)
	require.NotEmpty(t, created.SessionToken)
	require.NotEmpty(t, created.CSRFToken)
	require.NotContains(t, created.SessionToken, repo.sessions[HashSSOToken(created.SessionToken)].SessionHash)
	require.NotContains(t, created.CSRFToken, repo.sessions[HashSSOToken(created.SessionToken)].CSRFHash)
	require.Equal(t, now, repo.deletedExpiredAt)

	authenticated, err := service.AuthenticateSession(context.Background(), created.SessionToken, "", false)
	require.NoError(t, err)
	require.Equal(t, keyID, authenticated.Credential.ID)
	require.Equal(t, ownerID, authenticated.OwnerID)
	require.Equal(t, CredentialRoleMember, authenticated.Membership.Role)

	keys.keys[keyID].Role = CredentialRoleManager
	manager, err := service.AuthenticateSession(context.Background(), created.SessionToken, "", false)
	require.NoError(t, err)
	require.Equal(t, CredentialRoleManager, manager.Membership.Role)

	_, err = service.AuthenticateSession(context.Background(), created.SessionToken, "wrong", true)
	require.ErrorIs(t, err, ErrUserPortalCSRFInvalid)
	_, err = service.AuthenticateSession(context.Background(), created.SessionToken, created.CSRFToken, true)
	require.NoError(t, err)

	stored := repo.sessions[HashSSOToken(created.SessionToken)]
	require.Equal(t, now.Add(UserPortalSessionTTL), stored.ExpiresAt)

	keys.keys[keyID].Scopes = []string{"read", "write"}
	rotated, err := service.AuthenticateSession(context.Background(), created.SessionToken, "", false)
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write"}, rotated.Membership.Grants)
}

func TestUserPortalSessionServiceRejectsMissingOrExpiredCredentials(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	keyID := uuid.New()
	repo := &portalSessionRepositoryStub{sessions: map[string]*domain.UserPortalSession{
		HashSSOToken("expired"): {
			SessionHash:  HashSSOToken("expired"),
			CredentialID: keyID,
			CSRFHash:     HashSSOToken("csrf"),
			ExpiresAt:    now.Add(-time.Second),
		},
		HashSSOToken("active"): {
			SessionHash:  HashSSOToken("active"),
			CredentialID: keyID,
			CSRFHash:     HashSSOToken("csrf"),
			ExpiresAt:    now.Add(time.Hour),
		},
		HashSSOToken("boundary"): {
			SessionHash:  HashSSOToken("boundary"),
			CredentialID: keyID,
			CSRFHash:     HashSSOToken("csrf"),
			ExpiresAt:    now,
		},
	}}
	keys := &activePortalKeyRepositoryStub{keys: map[uuid.UUID]*domain.Credential{
		keyID: {ID: keyID, TeamID: uuid.New()},
	}}
	service := NewUserPortalSessionService(repo, keys, func() time.Time { return now })

	_, err := service.AuthenticateSession(context.Background(), "", "", false)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)
	_, err = service.AuthenticateSession(context.Background(), "expired", "csrf", false)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)
	_, err = service.AuthenticateSession(context.Background(), "boundary", "csrf", false)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)

	keys.keys[keyID] = nil
	_, err = service.AuthenticateSession(context.Background(), "active", "csrf", false)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)

	repo.deleteErr = errors.New("delete failed")
	require.Error(t, service.Logout(context.Background(), "expired"))
}

func TestUserPortalSessionServicePropagatesDependencyErrors(t *testing.T) {
	ctx := context.Background()
	keyID := uuid.New()
	dependencyErr := errors.New("dependency failed")
	repo := &portalSessionRepositoryStub{sessions: make(map[string]*domain.UserPortalSession)}
	keys := &activePortalKeyRepositoryStub{keys: map[uuid.UUID]*domain.Credential{
		keyID: {ID: keyID, TeamID: uuid.New()},
	}}
	portal := NewUserPortalSessionService(repo, keys, nil)

	keys.err = dependencyErr
	_, err := portal.CreateSession(ctx, keyID)
	require.ErrorIs(t, err, dependencyErr)
	keys.err = nil

	_, err = portal.CreateSession(ctx, uuid.Nil)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)
	_, err = NewUserPortalSessionService(nil, keys, nil).CreateSession(ctx, keyID)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)
	keys.keys[keyID] = nil
	_, err = portal.CreateSession(ctx, keyID)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)
	keys.keys[keyID] = &domain.Credential{ID: keyID, TeamID: uuid.New()}

	repo.deleteExpiredErr = dependencyErr
	_, err = portal.CreateSession(ctx, keyID)
	require.ErrorIs(t, err, dependencyErr)
	repo.deleteExpiredErr = nil
	repo.createErr = dependencyErr
	_, err = portal.CreateSession(ctx, keyID)
	require.ErrorIs(t, err, dependencyErr)
	repo.createErr = nil
	repo.sessions[HashSSOToken("session")] = &domain.UserPortalSession{
		SessionHash:  HashSSOToken("session"),
		CredentialID: keyID,
		CSRFHash:     HashSSOToken("csrf"),
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	}

	repo.getErr = dependencyErr
	_, err = portal.AuthenticateSession(ctx, "session", "", false)
	require.ErrorIs(t, err, dependencyErr)
	repo.getErr = nil
	keys.err = dependencyErr
	_, err = portal.AuthenticateSession(ctx, "session", "", false)
	require.ErrorIs(t, err, dependencyErr)

	var nilPortal *UserPortalSessionService
	_, err = nilPortal.CreateSession(ctx, keyID)
	require.ErrorIs(t, err, ErrUserPortalSessionInvalid)
	require.NoError(t, nilPortal.Logout(ctx, "session"))
	require.NoError(t, portal.Logout(ctx, ""))
	require.Nil(t, authenticatedActorFromCredential(nil))
}
