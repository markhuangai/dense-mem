package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOControlErrorBranches(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("backend failed")
	providerID := uuid.New()
	teamID := uuid.New()
	mappingID := uuid.New()
	validProvider := domain.SSOProvider{
		ID:        providerID,
		Name:      "Enterprise",
		Kind:      domain.SSOProviderKindGenericOIDC,
		IssuerURL: "https://issuer.example.com",
		ClientID:  "client-id",
		Enabled:   true,
	}
	validMapping := domain.SSOGroupMapping{
		ID:         mappingID,
		ProviderID: providerID,
		TeamID:     teamID,
		GroupID:    "dense-mem-admin",
		Scopes:     []string{CredentialScopeRead},
		Role:       CredentialRoleMember,
		Enabled:    true,
	}
	logger := &captureSSOLogger{}

	require.Equal(t, true, NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{
		CookieSecure:  true,
		RuntimeConfig: ssoRuntimeConfigStub{err: backendErr},
		Logger:        logger,
	}).CookieSecure(ctx))

	_, err := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{err: backendErr},
		Logger:        logger,
	}).ListEnabledProviders(ctx)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, createMappingErr: backendErr}, SSOConfig{Logger: logger}).CreateMapping(ctx, validMapping)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, listMappingsErr: backendErr}, SSOConfig{Logger: logger}).CreateMapping(ctx, validMapping)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger}).UpdateMapping(ctx, domain.SSOGroupMapping{})
	require.ErrorContains(t, err, "sso group mapping ID is required")

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger}).UpdateMapping(ctx, domain.SSOGroupMapping{ID: mappingID})
	require.ErrorContains(t, err, "sso provider ID is required")

	_, err = NewSSOService(&ssoRepositoryStub{t: t, updateMappingErr: backendErr}, SSOConfig{Logger: logger}).UpdateMapping(ctx, validMapping)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, listMappingsErr: backendErr}, SSOConfig{Logger: logger}).UpdateMapping(ctx, validMapping)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, updateProviderErr: backendErr}, SSOConfig{Logger: logger}).UpdateProvider(ctx, validProvider)
	require.ErrorIs(t, err, backendErr)

	assert.NotEmpty(t, logger.debugs)
}

func TestSSOCompleteLoginEarlyErrorBranches(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("backend failed")
	providerID := uuid.New()
	stateToken := "state-token"
	callbackURL := "https://app.example.com/ui/api/sso/callback"
	logger := &captureSSOLogger{}

	_, err := NewSSOService(nil, SSOConfig{Logger: logger}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{err: backendErr},
		Logger:        logger,
	}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, consumeOAuthStateErr: backendErr}, SSOConfig{Logger: logger}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	state := &domain.SSOOAuthState{StateHash: HashSSOToken(stateToken), ProviderID: providerID}
	_, err = NewSSOService(&ssoRepositoryStub{t: t, consumableState: state, getProviderErr: backendErr}, SSOConfig{Logger: logger}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, consumableState: state}, SSOConfig{Logger: logger}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	disabledRepo := &ssoRepositoryStub{
		t:               t,
		consumableState: state,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {ID: providerID, Name: "Enterprise", Enabled: false},
		},
	}
	_, err = NewSSOService(disabledRepo, SSOConfig{Logger: logger}).CompleteLogin(ctx, stateToken, "code", callbackURL)
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	assert.NotEmpty(t, logger.debugs)
}

func TestSSOBeginLoginErrorBranches(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("backend failed")
	providerID := uuid.New()
	callbackURL := "https://app.example.com/ui/api/sso/callback"
	logger := &captureSSOLogger{}

	_, err := NewSSOService(nil, SSOConfig{Logger: logger}).BeginLogin(ctx, providerID, callbackURL, "")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{err: backendErr},
		Logger:        logger,
	}).BeginLogin(ctx, providerID, callbackURL, "")
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, getProviderErr: backendErr}, SSOConfig{Logger: logger}).BeginLogin(ctx, providerID, callbackURL, "")
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger}).BeginLogin(ctx, providerID, callbackURL, "")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	discovery := httptest.NewServer(http.NotFoundHandler())
	defer discovery.Close()
	providers := map[uuid.UUID]*domain.SSOProvider{
		providerID: {
			ID:        providerID,
			Name:      "Enterprise",
			Kind:      domain.SSOProviderKindGenericOIDC,
			IssuerURL: discovery.URL,
			ClientID:  "client-id",
			Enabled:   true,
		},
	}
	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers}, SSOConfig{
		HTTPClient: discovery.Client(),
		Logger:     logger,
	}).BeginLogin(ctx, providerID, callbackURL, "")
	require.ErrorContains(t, err, "failed to discover oidc provider")

	oidcServer := ssoDiscoveryServer(t)
	defer oidcServer.Close()
	stateProvider := *providers[providerID]
	stateProvider.IssuerURL = oidcServer.URL
	_, err = NewSSOService(&ssoRepositoryStub{
		t:                   t,
		providers:           map[uuid.UUID]*domain.SSOProvider{providerID: &stateProvider},
		createOAuthStateErr: backendErr,
	}, SSOConfig{
		HTTPClient:    oidcServer.Client(),
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{StateTTL: time.Minute}},
		Logger:        logger,
	}).BeginLogin(ctx, providerID, callbackURL, "")
	require.ErrorIs(t, err, backendErr)

	assert.NotEmpty(t, logger.debugs)
}

func TestSSOCompleteLoginOIDCConfigFailure(t *testing.T) {
	ctx := context.Background()
	providerID := uuid.New()
	stateToken := "state-token"
	discovery := httptest.NewServer(http.NotFoundHandler())
	defer discovery.Close()
	repo := &ssoRepositoryStub{
		t:               t,
		consumableState: &domain.SSOOAuthState{StateHash: HashSSOToken(stateToken), ProviderID: providerID},
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Enterprise",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: discovery.URL,
				ClientID:  "client-id",
				Enabled:   true,
			},
		},
	}

	_, err := NewSSOService(repo, SSOConfig{
		HTTPClient: discovery.Client(),
		Logger:     &captureSSOLogger{},
	}).CompleteLogin(ctx, stateToken, "code", "https://app.example.com/ui/api/sso/callback")

	require.ErrorContains(t, err, "failed to discover oidc provider")
}

func TestSSOSessionErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	backendErr := errors.New("backend failed")
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	profileID := uuid.New()
	sessionToken := "session-token"
	sessionHash := HashSSOToken(sessionToken)
	session := &domain.SSOSession{
		SessionHash:  sessionHash,
		IdentityID:   identityID,
		ProviderID:   providerID,
		MembershipID: profileID,
		OwnerID:      profileID,
		TeamID:       teamID,
		CSRFHash:     HashSSOToken("csrf"),
		ExpiresAt:    now.Add(time.Hour),
	}
	logger := &captureSSOLogger{}
	validSessionConfig := SSOConfig{Logger: logger, Now: func() time.Time { return now }}

	svc := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger})
	_, err := svc.AuthenticateSession(ctx, "", "", false)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	svc = NewSSOService(&ssoRepositoryStub{t: t, getSessionErr: backendErr}, SSOConfig{Logger: logger})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, backendErr)

	svc = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	expired := *session
	expired.ExpiresAt = now.Add(-time.Second)
	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: &expired}}, SSOConfig{
		Logger: logger,
		Now:    func() time.Time { return now },
	})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}, getSSOProfileErr: backendErr}, validSessionConfig)
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, backendErr)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}}, validSessionConfig)
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}, getIdentityErr: backendErr}, validSessionConfig)
	_, err = svc.CurrentSession(ctx, sessionToken)
	require.ErrorIs(t, err, backendErr)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}}, validSessionConfig)
	_, err = svc.CurrentSession(ctx, sessionToken)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}, listTeamProfilesErr: backendErr, identities: map[uuid.UUID]*domain.SSOIdentity{identityID: {ID: identityID}}}, validSessionConfig)
	_, err = svc.CurrentSession(ctx, sessionToken)
	require.ErrorIs(t, err, backendErr)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}, listTeamProfilesErr: backendErr}, validSessionConfig)
	_, err = svc.SwitchSessionTeam(ctx, sessionToken, profileID)
	require.ErrorIs(t, err, backendErr)

	svc = NewSSOService(&ssoRepositoryStub{t: t, sessions: map[string]*domain.SSOSession{sessionHash: session}}, validSessionConfig)
	_, err = svc.SwitchSessionTeam(ctx, sessionToken, profileID)
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	teamProfile := ssoTeamProfile(identityID, providerID, "subject", teamID, profileID, "Team", []string{CredentialScopeRead}, CredentialRoleMember)
	switchRepo := &ssoRepositoryStub{
		t:        t,
		sessions: map[string]*domain.SSOSession{sessionHash: session},
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {ID: providerID, Name: "Enterprise", Enabled: true},
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject",
			Groups:     []string{"group-a"},
			Status:     "active",
			ExpiresAt:  now.Add(time.Hour),
		},
		mappings: []*domain.SSOGroupMapping{
			{ProviderID: providerID, TeamID: teamID, GroupID: "group-a", Scopes: []string{CredentialScopeRead}, Role: CredentialRoleMember, Enabled: true},
		},
		teamProfiles:     []*domain.SSOTeamMembership{teamProfile},
		updateSessionErr: backendErr,
	}
	_, err = NewSSOService(switchRepo, validSessionConfig).SwitchSessionTeam(ctx, sessionToken, teamID)
	require.ErrorIs(t, err, backendErr)

	svc = NewSSOService(&ssoRepositoryStub{t: t, deleteSessionErr: backendErr}, SSOConfig{Logger: logger})
	require.ErrorIs(t, svc.Logout(ctx, sessionToken), backendErr)

	assert.NotEmpty(t, logger.debugs)
}

func TestSSOValidateCredentialErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	backendErr := errors.New("backend failed")
	providerID := uuid.New()
	identityID := uuid.New()
	teamID := uuid.New()
	key := &domain.Credential{ID: uuid.New(), TeamID: teamID, Scopes: []string{CredentialScopeRead}, OwnerIdentityID: &identityID, SSOProviderID: &providerID, SSOSubject: "subject"}
	logger := &captureSSOLogger{}

	_, err := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger}).ValidateCredential(ctx, nil)
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, getProviderErr: backendErr}, SSOConfig{Logger: logger}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{Logger: logger}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	providers := map[uuid.UUID]*domain.SSOProvider{
		providerID: {ID: providerID, Name: "Enterprise", Enabled: true},
	}
	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers, cacheErr: backendErr}, SSOConfig{Logger: logger}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers}, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{err: backendErr},
		Logger:        logger,
	}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, backendErr)

	cache := &domain.SSOEntitlementCache{
		ProviderID: providerID,
		Subject:    "subject",
		Groups:     []string{"group-a"},
		Status:     "active",
		ExpiresAt:  now.Add(time.Hour),
	}
	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers, cache: cache, mappingsForGroupsErr: backendErr}, SSOConfig{
		Logger: logger,
		Now:    func() time.Time { return now },
	}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, backendErr)

	deniedCache := *cache
	deniedCache.Status = "denied"
	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers, cache: &deniedCache}, SSOConfig{
		Logger: logger,
		Now:    func() time.Time { return now },
	}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers, cache: cache}, SSOConfig{
		Logger: logger,
		Now:    func() time.Time { return now },
	}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers, mappingsForGroupsErr: backendErr}, SSOConfig{
		GroupResolver: &ssoGroupResolverStub{groups: []string{"group-a"}},
		Logger:        logger,
		Now:           func() time.Time { return now },
	}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, backendErr)

	_, err = NewSSOService(&ssoRepositoryStub{t: t, providers: providers, setCacheErr: backendErr}, SSOConfig{
		GroupResolver: &ssoGroupResolverStub{groups: []string{"group-a"}},
		Logger:        logger,
		Now:           func() time.Time { return now },
	}).ValidateCredential(ctx, key)
	require.ErrorIs(t, err, backendErr)

	assert.NotEmpty(t, logger.debugs)
}

func TestSSOStoreEntitlementCacheBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	logger := &captureSSOLogger{}

	repo := &ssoRepositoryStub{t: t}
	svc := NewSSOService(repo, SSOConfig{
		EntitlementCacheTTL: 2 * time.Minute,
		Logger:              logger,
		Now:                 func() time.Time { return now },
	})
	err := svc.storeEntitlementCache(ctx, providerID, "subject", []string{"group-b", "group-a", "group-a"}, "active", "")
	require.NoError(t, err)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, providerID, repo.savedCache.ProviderID)
	assert.Equal(t, "subject", repo.savedCache.Subject)
	assert.Equal(t, []string{"group-a", "group-b"}, repo.savedCache.Groups)
	assert.Equal(t, "active", repo.savedCache.Status)
	assert.Equal(t, now.Add(2*time.Minute), repo.savedCache.ExpiresAt)

	err = NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{err: errors.New("runtime failed")},
		Logger:        logger,
	}).storeEntitlementCache(ctx, providerID, "subject", nil, "error", "runtime failed")
	require.ErrorContains(t, err, "runtime failed")

	err = NewSSOService(&ssoRepositoryStub{t: t, setCacheErr: errors.New("cache failed")}, SSOConfig{
		Logger: logger,
		Now:    func() time.Time { return now },
	}).storeEntitlementCacheWithTTL(ctx, time.Minute, providerID, "subject", []string{"group-a"}, "error", "cache failed")
	require.ErrorContains(t, err, "cache failed")
	assert.NotEmpty(t, logger.debugs)
}
