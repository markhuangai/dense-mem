package access

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestControlIdentitySessionRequiresStillActiveDirectoryIdentityAndAdminGroup(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	sessionToken := "control-session-token"
	repo := &controlIdentityRepositoryStub{
		session: &domain.ControlSession{
			SessionHash: HashSSOToken(sessionToken),
			IdentityID:  identityID,
			ProviderID:  providerID,
			GroupIDs:    []string{"entra-control-admins"},
			CSRFHash:    HashSSOToken("csrf-token"),
			ExpiresAt:   now.Add(time.Hour),
		},
		groups: []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "entra-control-admins", Enabled: true}},
	}
	ssoRepo := &controlIdentitySSORepositoryStub{
		identity: &domain.SSOIdentity{ID: identityID, ProviderID: providerID, Active: true},
		provider: &domain.SSOProvider{ID: providerID, Enabled: true},
	}
	service := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{Now: func() time.Time { return now }})

	identity, err := service.AuthenticateSession(context.Background(), sessionToken, "csrf-token", true)
	require.NoError(t, err)
	require.Equal(t, identityID, identity.ID)

	repo.groups[0].Enabled = false
	_, err = service.AuthenticateSession(context.Background(), sessionToken, "", false)
	require.ErrorIs(t, err, ErrControlAccessDenied)

	repo.groups[0].Enabled = true
	ssoRepo.identity.Active = false
	_, err = service.AuthenticateSession(context.Background(), sessionToken, "", false)
	require.ErrorIs(t, err, ErrControlSessionInvalid)
}

func TestControlIdentitySessionRefreshesConfiguredAdminGroups(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	sessionToken := "control-session-token"
	repo := &controlIdentityRepositoryStub{
		session: &domain.ControlSession{
			SessionHash: HashSSOToken(sessionToken),
			IdentityID:  identityID,
			ProviderID:  providerID,
			GroupIDs:    []string{"entra-control-admins"},
			ExpiresAt:   now.Add(time.Hour),
		},
		groups: []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "entra-control-admins", Enabled: true}},
	}
	resolver := &controlIdentityGroupResolver{groups: []string{"entra-control-admins"}}
	ssoRepo := &controlIdentitySSORepositoryStub{
		identity: &domain.SSOIdentity{ID: identityID, ProviderID: providerID, Subject: "entra-user-1", Active: true},
		provider: &domain.SSOProvider{ID: providerID, Enabled: true, GroupsEndpoint: "https://graph.example.com/users/{subject}/getMemberGroups"},
	}
	service := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{GroupResolver: resolver, Now: func() time.Time { return now }})

	_, err := service.AuthenticateSession(context.Background(), sessionToken, "", false)
	require.NoError(t, err)
	require.Equal(t, "entra-user-1", resolver.subject)

	resolver.groups = nil
	_, err = service.AuthenticateSession(context.Background(), sessionToken, "", false)
	require.ErrorIs(t, err, ErrControlAccessDenied)
}

type controlIdentityRepositoryStub struct {
	repository.ControlIdentityRepository
	session           *domain.ControlSession
	sessions          map[string]*domain.ControlSession
	states            map[string]*domain.ControlOAuthState
	groups            []*domain.ControlAdminGroup
	deletedSession    string
	deletedStateAt    time.Time
	deletedSessionAt  time.Time
	listGroupsErr     error
	createGroupErr    error
	updateGroupErr    error
	retireGroupErr    error
	createStateErr    error
	consumeStateErr   error
	deleteStatesErr   error
	createSessionErr  error
	getSessionErr     error
	deleteSessionErr  error
	deleteSessionsErr error
}

func (r *controlIdentityRepositoryStub) GetControlSession(_ context.Context, hash string) (*domain.ControlSession, error) {
	if r.getSessionErr != nil {
		return nil, r.getSessionErr
	}
	if r.session != nil && r.session.SessionHash == hash {
		copy := *r.session
		copy.GroupIDs = append([]string(nil), r.session.GroupIDs...)
		return &copy, nil
	}
	if session := r.sessions[hash]; session != nil {
		copy := *session
		copy.GroupIDs = append([]string(nil), session.GroupIDs...)
		return &copy, nil
	}
	return nil, nil
}

func (r *controlIdentityRepositoryStub) ListControlAdminGroups(_ context.Context, providerID uuid.UUID) ([]*domain.ControlAdminGroup, error) {
	if r.listGroupsErr != nil {
		return nil, r.listGroupsErr
	}
	items := make([]*domain.ControlAdminGroup, 0, len(r.groups))
	for _, group := range r.groups {
		if group != nil && group.ProviderID == providerID {
			copy := *group
			items = append(items, &copy)
		}
	}
	return items, nil
}

func (r *controlIdentityRepositoryStub) CreateControlAdminGroup(_ context.Context, group *domain.ControlAdminGroup) error {
	if r.createGroupErr != nil {
		return r.createGroupErr
	}
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	copy := *group
	r.groups = append(r.groups, &copy)
	return nil
}

func (r *controlIdentityRepositoryStub) UpdateControlAdminGroup(_ context.Context, group *domain.ControlAdminGroup) error {
	if r.updateGroupErr != nil {
		return r.updateGroupErr
	}
	for index, existing := range r.groups {
		if existing != nil && existing.ID == group.ID && existing.ProviderID == group.ProviderID {
			copy := *group
			r.groups[index] = &copy
			return nil
		}
	}
	return nil
}

func (r *controlIdentityRepositoryStub) RetireControlAdminGroup(_ context.Context, providerID, groupID uuid.UUID) error {
	if r.retireGroupErr != nil {
		return r.retireGroupErr
	}
	for _, group := range r.groups {
		if group != nil && group.ID == groupID && group.ProviderID == providerID {
			group.Enabled = false
			now := time.Now().UTC()
			group.RetiredAt = &now
		}
	}
	return nil
}

func (r *controlIdentityRepositoryStub) CreateControlOAuthState(_ context.Context, state domain.ControlOAuthState) error {
	if r.createStateErr != nil {
		return r.createStateErr
	}
	if r.states == nil {
		r.states = make(map[string]*domain.ControlOAuthState)
	}
	copy := state
	r.states[state.StateHash] = &copy
	return nil
}

func (r *controlIdentityRepositoryStub) ConsumeControlOAuthState(_ context.Context, stateHash string) (*domain.ControlOAuthState, error) {
	if r.consumeStateErr != nil {
		return nil, r.consumeStateErr
	}
	state := r.states[stateHash]
	if state == nil {
		return nil, nil
	}
	delete(r.states, stateHash)
	copy := *state
	return &copy, nil
}

func (r *controlIdentityRepositoryStub) DeleteExpiredControlOAuthStates(_ context.Context, now time.Time) error {
	if r.deleteStatesErr != nil {
		return r.deleteStatesErr
	}
	r.deletedStateAt = now
	for stateHash, state := range r.states {
		if !state.ExpiresAt.After(now) {
			delete(r.states, stateHash)
		}
	}
	return nil
}

func (r *controlIdentityRepositoryStub) CreateControlSession(_ context.Context, session domain.ControlSession) error {
	if r.createSessionErr != nil {
		return r.createSessionErr
	}
	if r.sessions == nil {
		r.sessions = make(map[string]*domain.ControlSession)
	}
	copy := session
	copy.GroupIDs = append([]string(nil), session.GroupIDs...)
	r.sessions[session.SessionHash] = &copy
	return nil
}

func (r *controlIdentityRepositoryStub) DeleteControlSession(_ context.Context, sessionHash string) error {
	if r.deleteSessionErr != nil {
		return r.deleteSessionErr
	}
	r.deletedSession = sessionHash
	delete(r.sessions, sessionHash)
	return nil
}

func (r *controlIdentityRepositoryStub) DeleteExpiredControlSessions(_ context.Context, now time.Time) error {
	if r.deleteSessionsErr != nil {
		return r.deleteSessionsErr
	}
	r.deletedSessionAt = now
	for sessionHash, session := range r.sessions {
		if !session.ExpiresAt.After(now) {
			delete(r.sessions, sessionHash)
		}
	}
	return nil
}

type controlIdentitySSORepositoryStub struct {
	repository.SSORepository
	identity          *domain.SSOIdentity
	provider          *domain.SSOProvider
	identities        map[uuid.UUID]*domain.SSOIdentity
	providers         map[uuid.UUID]*domain.SSOProvider
	listProvidersErr  error
	getProviderErr    error
	upsertIdentityErr error
	getIdentityErr    error
}

func (r *controlIdentitySSORepositoryStub) GetIdentity(_ context.Context, id uuid.UUID) (*domain.SSOIdentity, error) {
	if r.getIdentityErr != nil {
		return nil, r.getIdentityErr
	}
	if identity := r.identities[id]; identity != nil {
		copy := *identity
		return &copy, nil
	}
	if r.identity == nil || r.identity.ID != id {
		return nil, nil
	}
	copy := *r.identity
	return &copy, nil
}

func (r *controlIdentitySSORepositoryStub) GetProvider(_ context.Context, id uuid.UUID) (*domain.SSOProvider, error) {
	if r.getProviderErr != nil {
		return nil, r.getProviderErr
	}
	if provider := r.providers[id]; provider != nil {
		copy := *provider
		return &copy, nil
	}
	if r.provider == nil || r.provider.ID != id {
		return nil, nil
	}
	copy := *r.provider
	return &copy, nil
}

func (r *controlIdentitySSORepositoryStub) ListEnabledProviders(context.Context) ([]*domain.SSOProvider, error) {
	if r.listProvidersErr != nil {
		return nil, r.listProvidersErr
	}
	items := make([]*domain.SSOProvider, 0, len(r.providers)+1)
	for _, provider := range r.providers {
		copy := *provider
		items = append(items, &copy)
	}
	if r.provider != nil && len(r.providers) == 0 {
		copy := *r.provider
		items = append(items, &copy)
	}
	return items, nil
}

func (r *controlIdentitySSORepositoryStub) UpsertIdentity(_ context.Context, identity *domain.SSOIdentity) error {
	if r.upsertIdentityErr != nil {
		return r.upsertIdentityErr
	}
	if identity.ID == uuid.Nil {
		identity.ID = uuid.New()
	}
	if r.identities == nil {
		r.identities = make(map[uuid.UUID]*domain.SSOIdentity)
	}
	copy := *identity
	r.identities[identity.ID] = &copy
	r.identity = &copy
	return nil
}

type controlIdentityGroupResolver struct {
	groups  []string
	subject string
	err     error
}

func (r *controlIdentityGroupResolver) ResolveGroups(_ context.Context, _ domain.SSOProvider, subject, _ string) ([]string, error) {
	r.subject = subject
	if r.err != nil {
		return nil, r.err
	}
	return append([]string(nil), r.groups...), nil
}
