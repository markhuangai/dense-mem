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

func TestControlIdentityServiceFiltersProvidersAndRejectsInvalidAdministration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	allowedProviderID := uuid.New()
	disabledProviderID := uuid.New()
	retiredProviderID := uuid.New()
	withoutGroupProviderID := uuid.New()
	retiredAt := time.Now().UTC()
	repo := &controlIdentityRepositoryStub{groups: []*domain.ControlAdminGroup{{ProviderID: allowedProviderID, GroupID: "admins", Enabled: true}}}
	ssoRepo := &controlIdentitySSORepositoryStub{providers: map[uuid.UUID]*domain.SSOProvider{
		allowedProviderID:      {ID: allowedProviderID, Name: "Allowed", Enabled: true},
		disabledProviderID:     {ID: disabledProviderID, Name: "Disabled", Enabled: false},
		retiredProviderID:      {ID: retiredProviderID, Name: "Retired", Enabled: true, RetiredAt: &retiredAt},
		withoutGroupProviderID: {ID: withoutGroupProviderID, Name: "No group", Enabled: true},
	}}
	svc := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test"}},
	})

	providers, err := svc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, allowedProviderID, providers[0].ID)

	_, err = svc.ListAdminGroups(ctx, uuid.Nil)
	require.ErrorContains(t, err, "required")
	_, err = svc.CreateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: uuid.New(), GroupID: "admins", Enabled: true})
	require.ErrorContains(t, err, "not found")
	_, err = svc.CreateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: retiredProviderID, GroupID: "admins", Enabled: true})
	require.ErrorContains(t, err, "not found")
	_, err = svc.UpdateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: allowedProviderID, GroupID: "admins", Enabled: true})
	require.ErrorContains(t, err, "required")
	_, err = svc.UpdateAdminGroup(ctx, domain.ControlAdminGroup{ID: uuid.New(), ProviderID: uuid.Nil, GroupID: "admins", Enabled: true})
	require.ErrorContains(t, err, "required")
	require.ErrorContains(t, svc.RetireAdminGroup(ctx, uuid.Nil, uuid.New()), "required")
	require.ErrorContains(t, svc.RetireAdminGroup(ctx, allowedProviderID, uuid.Nil), "required")
}

func TestControlIdentityServiceUsesDefaultClockAndFailsClosedOnRuntimeFailure(t *testing.T) {
	t.Parallel()

	defaulted := NewControlIdentityService(nil, nil, ControlIdentityConfig{})
	require.False(t, defaulted.now().IsZero())
	providers, err := defaulted.ListEnabledProviders(context.Background())
	require.NoError(t, err)
	require.Empty(t, providers)

	runtimeFailure := NewControlIdentityService(
		&controlIdentityRepositoryStub{},
		&controlIdentitySSORepositoryStub{},
		ControlIdentityConfig{RuntimeConfig: ssoRuntimeConfigStub{err: errors.New("runtime unavailable")}},
	)
	providers, err = runtimeFailure.ListEnabledProviders(context.Background())
	require.ErrorContains(t, err, "runtime unavailable")
	require.Nil(t, providers)
}

func TestControlIdentityServiceFailsClosedBeforeOIDCExchange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	providerID := uuid.New()
	now := time.Now().UTC()
	runtime := ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test", StateTTL: time.Minute, SessionTTL: time.Hour}}
	validProvider := &domain.SSOProvider{ID: providerID, Name: "Control", Kind: domain.SSOProviderKindGenericOIDC, ClientID: "control-client", Enabled: true}
	repo := &controlIdentityRepositoryStub{groups: []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}}, states: make(map[string]*domain.ControlOAuthState)}
	ssoRepo := &controlIdentitySSORepositoryStub{provider: validProvider}
	svc := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{RuntimeConfig: runtime, Now: func() time.Time { return now }})

	_, err := svc.BeginLogin(ctx, providerID, "")
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	_, err = svc.BeginLogin(ctx, uuid.New(), "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)
	_, err = svc.CompleteLogin(ctx, "", "code", "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)
	_, err = svc.CompleteLogin(ctx, "state", "", "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)
	_, err = svc.CompleteLogin(ctx, "state", "code", "")
	require.ErrorIs(t, err, ErrControlAccessDenied)
	_, err = svc.CompleteLogin(ctx, "missing-state", "code", "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)

	disabled := *validProvider
	disabled.Enabled = false
	ssoRepo.provider = &disabled
	repo.states[HashSSOToken("disabled-state")] = &domain.ControlOAuthState{ProviderID: providerID, ExpiresAt: now.Add(time.Minute)}
	_, err = svc.CompleteLogin(ctx, "disabled-state", "code", "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)

	ssoRepo.provider = validProvider
	repo.states[HashSSOToken("invalid-discovery-state")] = &domain.ControlOAuthState{ProviderID: providerID, ExpiresAt: now.Add(time.Minute)}
	_, err = svc.CompleteLogin(ctx, "invalid-discovery-state", "code", "https://control.example.test/control/auth/callback")
	require.Error(t, err)

	noGroupsRepo := &controlIdentityRepositoryStub{}
	noGroupsService := NewControlIdentityService(noGroupsRepo, &controlIdentitySSORepositoryStub{provider: validProvider}, ControlIdentityConfig{RuntimeConfig: runtime})
	_, err = noGroupsService.BeginLogin(ctx, providerID, "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)
}

func TestControlIdentityServiceSessionAndTransportValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	providerID := uuid.New()
	identityID := uuid.New()
	sessionToken := "control-session"
	provider := &domain.SSOProvider{ID: providerID, Name: "Control", Enabled: true}
	repo := &controlIdentityRepositoryStub{
		session: &domain.ControlSession{SessionHash: HashSSOToken(sessionToken), IdentityID: identityID, ProviderID: providerID, GroupIDs: []string{"admins", "admins"}, ExpiresAt: now.Add(time.Hour)},
		groups:  []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}, {ProviderID: providerID, GroupID: "retired", Enabled: true, RetiredAt: &now}, {ProviderID: providerID, GroupID: "", Enabled: true}},
	}
	ssoRepo := &controlIdentitySSORepositoryStub{identity: &domain.SSOIdentity{ID: identityID, ProviderID: providerID, Subject: "subject", Active: true}, provider: provider}
	svc := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{Now: func() time.Time { return now }})

	_, err := svc.AuthenticateSession(ctx, "", "", false)
	require.ErrorIs(t, err, ErrControlSessionInvalid)
	_, err = svc.AuthenticateSession(ctx, "missing", "", false)
	require.ErrorIs(t, err, ErrControlSessionInvalid)
	identity, err := svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.NoError(t, err)
	require.Equal(t, identityID, identity.ID)

	ssoRepo.identity = nil
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrControlSessionInvalid)
	ssoRepo.identity = &domain.SSOIdentity{ID: identityID, ProviderID: uuid.New(), Active: true}
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrControlSessionInvalid)

	ssoRepo.identity = &domain.SSOIdentity{ID: identityID, ProviderID: providerID, Subject: "subject", Active: true}
	provider.GroupsEndpoint = "https://groups.example.test/{subject}"
	resolver := &controlIdentityGroupResolver{err: errors.New("groups unavailable")}
	svc = NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{GroupResolver: resolver, Now: func() time.Time { return now }})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrControlAccessDenied)

	provider.GroupsEndpoint = ""
	groups, err := svc.currentSessionGroups(ctx, *ssoRepo.identity, []string{"admins", "admins"})
	require.NoError(t, err)
	require.Equal(t, []string{"admins"}, groups)
	ssoRepo.provider = nil
	_, err = svc.currentSessionGroups(ctx, *ssoRepo.identity, nil)
	require.ErrorIs(t, err, ErrControlAccessDenied)

	deadlineCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	providerCtx, providerCancel := svc.providerContext(deadlineCtx, SSORuntimeConfig{HTTPTimeout: time.Second})
	defer providerCancel()
	expectedDeadline, expectedDeadlineSet := deadlineCtx.Deadline()
	actualDeadline, actualDeadlineSet := providerCtx.Deadline()
	require.Equal(t, expectedDeadlineSet, actualDeadlineSet)
	require.Equal(t, expectedDeadline, actualDeadline)
	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	providerCtx, providerCancel = svc.providerContext(canceledCtx, SSORuntimeConfig{HTTPTimeout: time.Second})
	defer providerCancel()
	require.ErrorIs(t, providerCtx.Err(), context.Canceled)

	discovery := ssoDiscoveryServer(t)
	defer discovery.Close()
	provider = &domain.SSOProvider{ID: providerID, Name: "Control", Kind: domain.SSOProviderKindGenericOIDC, IssuerURL: discovery.URL, ClientID: "control-client", ClientSecretEnv: "DENSE_MEM_CONTROL_IDENTITY_TEST_SECRET", Enabled: true}
	t.Setenv(provider.ClientSecretEnv, "test-secret")
	_, oauthConfig, err := svc.oauthConfig(ctx, *provider, "https://control.example.test/control/auth/callback")
	require.NoError(t, err)
	require.Equal(t, "test-secret", oauthConfig.ClientSecret)
	_, _, err = svc.oauthConfig(ctx, domain.SSOProvider{}, "https://control.example.test/control/auth/callback")
	require.Error(t, err)
	require.ErrorContains(t, normalizeControlAdminGroup(nil), "required")
	require.ErrorContains(t, normalizeControlAdminGroup(&domain.ControlAdminGroup{ProviderID: providerID}), "invalid")
}
