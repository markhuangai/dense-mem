package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestControlIdentityServiceAdminGroupsAndOIDCLogin(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	providerID := uuid.New()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	var nonce string
	var oidcServer *httptest.Server
	oidcServer = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, request *nethttp.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 oidcServer.URL,
				"authorization_endpoint": oidcServer.URL + "/authorize",
				"token_endpoint":         oidcServer.URL + "/token",
				"jwks_uri":               oidcServer.URL + "/jwks",
				"userinfo_endpoint":      oidcServer.URL + "/userinfo",
			}))
		case "/jwks":
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{rsaJWK(&privateKey.PublicKey, "control-key")}}))
		case "/token":
			require.NoError(t, request.ParseForm())
			require.Equal(t, "authorization_code", request.Form.Get("grant_type"))
			idToken := signedOIDCToken(t, privateKey, "control-key", map[string]any{
				"iss":    oidcServer.URL,
				"sub":    "entra-control-user",
				"oid":    "entra-control-user",
				"tid":    "tenant-id",
				"aud":    "control-client",
				"exp":    now.Add(time.Hour).Unix(),
				"iat":    now.Add(-time.Minute).Unix(),
				"nonce":  nonce,
				"email":  "admin@example.test",
				"name":   "Control Admin",
				"groups": []string{"entra-control-admins"},
			})
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"access_token": "control-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"id_token":     idToken,
			}))
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"sub": "entra-control-user"}))
		default:
			nethttp.NotFound(w, request)
		}
	}))
	defer oidcServer.Close()

	provider := &domain.SSOProvider{
		ID:        providerID,
		Name:      "Microsoft Entra ID",
		Kind:      domain.SSOProviderKindAzureAD,
		IssuerURL: oidcServer.URL,
		TenantID:  "tenant-id",
		ClientID:  "control-client",
		Enabled:   true,
	}
	repo := &controlIdentityRepositoryStub{
		groups: []*domain.ControlAdminGroup{{
			ID:         uuid.New(),
			ProviderID: providerID,
			GroupID:    "entra-control-admins",
			GroupName:  "Control administrators",
			Enabled:    true,
		}},
		states:   make(map[string]*domain.ControlOAuthState),
		sessions: make(map[string]*domain.ControlSession),
	}
	ssoRepo := &controlIdentitySSORepositoryStub{providers: map[uuid.UUID]*domain.SSOProvider{providerID: provider}}
	svc := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{
		HTTPClient: oidcServer.Client(),
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{
			ControlPublicBaseURL: "https://control.example.test",
			StateTTL:             time.Minute,
			SessionTTL:           time.Hour,
			HTTPTimeout:          5 * time.Second,
		}},
		Now: func() time.Time { return now },
	})

	providers, err := svc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, providerID, providers[0].ID)

	_, err = svc.CreateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: providerID, GroupName: "missing ID"})
	require.ErrorContains(t, err, "invalid")
	createdGroup, err := svc.CreateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: providerID, GroupID: "entra-break-glass", GroupName: "Break glass", Enabled: true})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, createdGroup.ID)
	createdGroup.GroupName = "Break-glass administrators"
	updatedGroup, err := svc.UpdateAdminGroup(ctx, *createdGroup)
	require.NoError(t, err)
	require.Equal(t, "Break-glass administrators", updatedGroup.GroupName)
	require.NoError(t, svc.RetireAdminGroup(ctx, providerID, createdGroup.ID))
	groups, err := svc.ListAdminGroups(ctx, providerID)
	require.NoError(t, err)
	require.Len(t, groups, 2)

	start, err := svc.BeginLogin(ctx, providerID, "https://control.example.test/control/auth/callback")
	require.NoError(t, err)
	startURL, err := url.Parse(start.AuthURL)
	require.NoError(t, err)
	stateToken := startURL.Query().Get("state")
	require.NotEmpty(t, stateToken)
	state := repo.states[HashSSOToken(stateToken)]
	require.NotNil(t, state)
	nonce = state.Nonce
	require.Equal(t, now, repo.deletedStateAt)

	result, err := svc.CompleteLogin(ctx, stateToken, "control-auth-code", "https://control.example.test/control/auth/callback")
	require.NoError(t, err)
	require.NotEmpty(t, result.SessionToken)
	require.NotEmpty(t, result.CSRFToken)
	require.Equal(t, "entra-control-user", result.Identity.Subject)
	require.Equal(t, now.Add(time.Hour), result.ExpiresAt)
	require.Equal(t, now, repo.deletedSessionAt)

	identity, err := svc.AuthenticateSession(ctx, result.SessionToken, result.CSRFToken, true)
	require.NoError(t, err)
	require.Equal(t, result.Identity.ID, identity.ID)
	_, err = svc.AuthenticateSession(ctx, result.SessionToken, "wrong-csrf", true)
	require.ErrorIs(t, err, ErrControlCSRFInvalid)
	current, err := svc.CurrentSession(ctx, result.SessionToken)
	require.NoError(t, err)
	require.Equal(t, identity.ID, current.ID)
	require.NoError(t, svc.Logout(ctx, result.SessionToken))
	require.Equal(t, HashSSOToken(result.SessionToken), repo.deletedSession)

	publicURL, err := svc.ControlPublicBaseURL(ctx)
	require.NoError(t, err)
	require.Equal(t, "https://control.example.test", publicURL)
	require.True(t, svc.CookieSecure(ctx))
	_, err = svc.BeginLogin(ctx, uuid.New(), "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlAccessDenied)
}

func TestControlIdentityServiceRequiresHTTPSControlIngress(t *testing.T) {
	t.Parallel()

	providerID := uuid.New()
	repo := &controlIdentityRepositoryStub{groups: []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}}}
	ssoRepo := &controlIdentitySSORepositoryStub{provider: &domain.SSOProvider{ID: providerID, Enabled: true}}
	svc := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "http://control.example.test"}},
	})

	providers, err := svc.ListEnabledProviders(context.Background())
	require.NoError(t, err)
	require.Empty(t, providers)
	require.False(t, svc.CookieSecure(context.Background()))
	_, err = svc.BeginLogin(context.Background(), providerID, "http://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	require.False(t, controlRuntimeReady(SSORuntimeConfig{ControlPublicBaseURL: "https://"}))
}

func TestControlIdentityServiceSafeFailureAndRefreshPaths(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	providerID := uuid.New()
	identityID := uuid.New()
	var unavailable *ControlIdentityService
	providers, err := unavailable.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Empty(t, providers)
	_, err = unavailable.ListAdminGroups(ctx, providerID)
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	_, err = unavailable.CreateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: providerID, GroupID: "admins"})
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	_, err = unavailable.UpdateAdminGroup(ctx, domain.ControlAdminGroup{ID: uuid.New(), ProviderID: providerID, GroupID: "admins"})
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	require.ErrorIs(t, unavailable.RetireAdminGroup(ctx, providerID, uuid.New()), ErrControlSSOUnavailable)
	_, err = unavailable.BeginLogin(ctx, providerID, "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	_, err = unavailable.CompleteLogin(ctx, "state", "code", "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, ErrControlSSOUnavailable)
	require.NoError(t, unavailable.Logout(ctx, ""))

	now := time.Now().UTC()
	repo := &controlIdentityRepositoryStub{
		session: &domain.ControlSession{SessionHash: HashSSOToken("expired"), IdentityID: identityID, ProviderID: providerID, ExpiresAt: now.Add(-time.Minute)},
		groups:  []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}, nil},
	}
	resolver := &controlIdentityGroupResolver{groups: []string{"admins", "admins"}}
	ssoRepo := &controlIdentitySSORepositoryStub{
		identity: &domain.SSOIdentity{ID: identityID, ProviderID: providerID, Subject: "subject", Active: true},
		provider: &domain.SSOProvider{ID: providerID, Enabled: true, GroupsEndpoint: "https://groups.example.test/{subject}"},
	}
	svc := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test", HTTPTimeout: time.Second}},
		GroupResolver: resolver,
		Now:           func() time.Time { return now },
	})
	_, err = svc.sessionFromToken(ctx, "expired")
	require.ErrorIs(t, err, ErrControlSessionInvalid)
	groups, err := svc.currentSessionGroups(ctx, *ssoRepo.identity, []string{"fallback", "fallback"})
	require.NoError(t, err)
	require.Equal(t, []string{"admins"}, groups)
	require.Equal(t, "subject", resolver.subject)
	providerCtx, cancel := svc.providerContext(nil, SSORuntimeConfig{HTTPTimeout: time.Second})
	defer cancel()
	require.NotNil(t, providerCtx.Value(oauth2.HTTPClient))

	invalidRuntime := NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{RuntimeConfig: ssoRuntimeConfigStub{err: errors.New("config unavailable")}})
	_, err = invalidRuntime.ControlPublicBaseURL(ctx)
	require.ErrorContains(t, err, "config unavailable")
	require.True(t, invalidRuntime.CookieSecure(ctx))
	_, err = invalidRuntime.BeginLogin(ctx, providerID, "https://control.example.test/control/auth/callback")
	require.ErrorContains(t, err, "config unavailable")

	require.ErrorContains(t, normalizeControlAdminGroup(&domain.ControlAdminGroup{ProviderID: providerID, GroupID: strings.Repeat("a", 513)}), "invalid")
	require.False(t, controlGroupsMatch([]*domain.ControlAdminGroup{{GroupID: "admins"}}, []string{"other"}))
}

func TestControlIdentityServiceSurfacesRepositoryFailures(t *testing.T) {
	ctx := context.Background()
	backendErr := errors.New("control repository failed")
	providerID := uuid.New()
	now := time.Now().UTC()
	provider := &domain.SSOProvider{ID: providerID, Name: "Enterprise", Kind: domain.SSOProviderKindGenericOIDC, ClientID: "control-client", Enabled: true}
	newService := func(repo *controlIdentityRepositoryStub, ssoRepo *controlIdentitySSORepositoryStub) *ControlIdentityService {
		return NewControlIdentityService(repo, ssoRepo, ControlIdentityConfig{
			RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test", StateTTL: time.Minute, SessionTTL: time.Hour}},
			Now:           func() time.Time { return now },
		})
	}

	svc := newService(&controlIdentityRepositoryStub{groups: []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}}}, &controlIdentitySSORepositoryStub{provider: provider, listProvidersErr: backendErr})
	_, err := svc.ListEnabledProviders(ctx)
	require.ErrorIs(t, err, backendErr)

	svc = newService(&controlIdentityRepositoryStub{listGroupsErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	_, err = svc.ListAdminGroups(ctx, providerID)
	require.ErrorIs(t, err, backendErr)

	svc = newService(&controlIdentityRepositoryStub{createGroupErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	_, err = svc.CreateAdminGroup(ctx, domain.ControlAdminGroup{ProviderID: providerID, GroupID: "admins", Enabled: true})
	require.ErrorIs(t, err, backendErr)

	svc = newService(&controlIdentityRepositoryStub{updateGroupErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	_, err = svc.UpdateAdminGroup(ctx, domain.ControlAdminGroup{ID: uuid.New(), ProviderID: providerID, GroupID: "admins", Enabled: true})
	require.ErrorIs(t, err, backendErr)

	svc = newService(&controlIdentityRepositoryStub{retireGroupErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	require.ErrorIs(t, svc.RetireAdminGroup(ctx, providerID, uuid.New()), backendErr)

	discovery := ssoDiscoveryServer(t)
	defer discovery.Close()
	provider.IssuerURL = discovery.URL
	svc = NewControlIdentityService(&controlIdentityRepositoryStub{
		groups:          []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}},
		deleteStatesErr: backendErr,
	}, &controlIdentitySSORepositoryStub{provider: provider}, ControlIdentityConfig{
		HTTPClient:    discovery.Client(),
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test", StateTTL: time.Minute}},
		Now:           func() time.Time { return now },
	})
	_, err = svc.BeginLogin(ctx, providerID, "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, backendErr)

	svc = NewControlIdentityService(&controlIdentityRepositoryStub{
		groups:         []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}},
		createStateErr: backendErr,
	}, &controlIdentitySSORepositoryStub{provider: provider}, ControlIdentityConfig{
		HTTPClient:    discovery.Client(),
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test", StateTTL: time.Minute}},
		Now:           func() time.Time { return now },
	})
	_, err = svc.BeginLogin(ctx, providerID, "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, backendErr)

	svc = newService(&controlIdentityRepositoryStub{consumeStateErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	_, err = svc.CompleteLogin(ctx, "state", "code", "https://control.example.test/control/auth/callback")
	require.ErrorIs(t, err, backendErr)

	sessionToken := "session"
	svc = newService(&controlIdentityRepositoryStub{getSessionErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, backendErr)

	repo := &controlIdentityRepositoryStub{session: &domain.ControlSession{SessionHash: HashSSOToken(sessionToken), IdentityID: uuid.New(), ProviderID: providerID, ExpiresAt: now.Add(time.Hour)}}
	svc = newService(repo, &controlIdentitySSORepositoryStub{provider: provider, getIdentityErr: backendErr})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, backendErr)

	svc = newService(&controlIdentityRepositoryStub{deleteSessionErr: backendErr}, &controlIdentitySSORepositoryStub{provider: provider})
	require.ErrorIs(t, svc.Logout(ctx, sessionToken), backendErr)
}
