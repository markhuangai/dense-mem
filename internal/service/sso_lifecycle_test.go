package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSSOBeginLoginCreatesOAuthState(t *testing.T) {
	var oidcServer *httptest.Server
	oidcServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 oidcServer.URL,
				"authorization_endpoint": oidcServer.URL + "/authorize",
				"token_endpoint":         oidcServer.URL + "/token",
				"jwks_uri":               oidcServer.URL + "/jwks",
				"userinfo_endpoint":      oidcServer.URL + "/userinfo",
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oidcServer.Close()

	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Enterprise",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: oidcServer.URL,
				ClientID:  "client-id",
				Scopes:    []string{"profile"},
				Enabled:   true,
			},
		},
	}
	svc := NewSSOService(repo, SSOConfig{
		HTTPClient:    oidcServer.Client(),
		StateTTL:      time.Minute,
		RuntimeConfig: ssoRuntimeConfigStub{cfg: SSORuntimeConfig{StateTTL: 2 * time.Minute}},
		Now:           func() time.Time { return now },
	})

	start, err := svc.BeginLogin(context.Background(), providerID, "https://app.example.com/ui/api/sso/callback", "//evil.example")

	require.NoError(t, err)
	assert.Equal(t, now, repo.deleteExpiredAt)
	require.Len(t, repo.oauthStates, 1)
	assert.Equal(t, providerID, repo.oauthStates[0].ProviderID)
	assert.Equal(t, "/ui", repo.oauthStates[0].RedirectPath)
	assert.Equal(t, now.Add(2*time.Minute), repo.oauthStates[0].ExpiresAt)
	assert.NotEmpty(t, repo.oauthStates[0].PKCEVerifier)
	assert.NotEmpty(t, repo.oauthStates[0].Nonce)

	authURL, err := url.Parse(start.AuthURL)
	require.NoError(t, err)
	assert.Equal(t, oidcServer.URL, authURL.Scheme+"://"+authURL.Host)
	assert.Equal(t, "/authorize", authURL.Path)
	query := authURL.Query()
	assert.Equal(t, "client-id", query.Get("client_id"))
	assert.Equal(t, "https://app.example.com/ui/api/sso/callback", query.Get("redirect_uri"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.NotEmpty(t, query.Get("state"))
	assert.NotEmpty(t, query.Get("nonce"))
	assert.Contains(t, strings.Fields(query.Get("scope")), "openid")
}

type ssoRuntimeConfigStub struct {
	cfg SSORuntimeConfig
	err error
}

func (s ssoRuntimeConfigStub) SSORuntimeConfig(context.Context) (SSORuntimeConfig, error) {
	if s.err != nil {
		return SSORuntimeConfig{}, s.err
	}
	return s.cfg, nil
}

func TestSSOBeginLoginFailsWhenExpiredStateCleanupFails(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	backendErr := errors.New("cleanup failed")
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Enterprise",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: "https://issuer.example.com",
				ClientID:  "client-id",
				Enabled:   true,
			},
		},
		deleteExpiredErr: backendErr,
	}
	svc := NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})

	start, err := svc.BeginLogin(context.Background(), providerID, "https://app.example.com/ui/api/sso/callback", "")

	require.ErrorIs(t, err, backendErr)
	require.Nil(t, start)
	assert.Equal(t, now, repo.deleteExpiredAt)
	assert.Empty(t, repo.oauthStates)
}

func TestSSOBeginLoginUsesBoundedOIDCHTTPTimeout(t *testing.T) {
	var slowDiscovery *httptest.Server
	slowDiscovery = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 slowDiscovery.URL,
			"authorization_endpoint": slowDiscovery.URL + "/authorize",
			"token_endpoint":         slowDiscovery.URL + "/token",
			"jwks_uri":               slowDiscovery.URL + "/jwks",
		})
	}))
	defer slowDiscovery.Close()

	providerID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Enterprise",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: slowDiscovery.URL,
				ClientID:  "client-id",
				Enabled:   true,
			},
		},
	}
	svc := NewSSOService(repo, SSOConfig{
		HTTPClient:  slowDiscovery.Client(),
		HTTPTimeout: 20 * time.Millisecond,
	})

	start, err := svc.BeginLogin(context.Background(), providerID, "https://app.example.com/ui/api/sso/callback", "")

	require.Error(t, err)
	require.Nil(t, start)
	require.ErrorContains(t, err, "context deadline exceeded")
}

func TestSSOCompleteLoginCreatesSession(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	providerID := uuid.New()
	teamID := uuid.New()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	var oidcServer *httptest.Server
	oidcServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{rsaJWK(&privateKey.PublicKey, "test-key")}}))
		case "/token":
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			assert.Equal(t, "auth-code", r.Form.Get("code"))
			assert.Equal(t, "pkce-verifier", r.Form.Get("code_verifier"))
			idToken := signedOIDCToken(t, privateKey, "test-key", map[string]any{
				"iss":    oidcServer.URL,
				"sub":    "subject-123",
				"aud":    "client-id",
				"exp":    now.Add(time.Hour).Unix(),
				"iat":    now.Add(-time.Minute).Unix(),
				"nonce":  "nonce-123",
				"email":  "ada@example.com",
				"name":   "Ada Lovelace",
				"groups": []string{"group-a"},
			})
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"id_token":     idToken,
			}))
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"sub": "subject-123"}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oidcServer.Close()

	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Enterprise",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: oidcServer.URL,
				ClientID:  "client-id",
				Scopes:    []string{"openid", "profile", "email"},
				Enabled:   true,
			},
		},
		consumableState: &domain.SSOOAuthState{
			StateHash:    HashSSOToken("state-token"),
			ProviderID:   providerID,
			PKCEVerifier: "pkce-verifier",
			Nonce:        "nonce-123",
			RedirectPath: "/ui/after-login",
			ExpiresAt:    now.Add(time.Minute),
		},
		mappings: []*domain.SSOGroupMapping{
			{
				ProviderID: providerID,
				TeamID:     teamID,
				TeamName:   "Enterprise Team",
				GroupID:    "group-a",
				Scopes:     []string{APIKeyScopeRead, APIKeyScopeWrite},
				Role:       APIKeyRoleManager,
				Enabled:    true,
			},
		},
	}
	svc := NewSSOService(repo, SSOConfig{
		HTTPClient:   oidcServer.Client(),
		SessionTTL:   time.Hour,
		Now:          func() time.Time { return now },
		CookieSecure: true,
	})

	result, err := svc.CompleteLogin(ctx, "state-token", "auth-code", "https://app.example.com/ui/api/sso/callback")

	require.NoError(t, err)
	assert.Equal(t, "/ui/after-login", result.RedirectPath)
	assert.NotEmpty(t, result.SessionToken)
	assert.NotEmpty(t, result.CSRFToken)
	assert.Equal(t, "ada@example.com", result.Session.Identity.Email)
	assert.Equal(t, "Ada Lovelace", result.Session.Identity.DisplayName)
	assert.Equal(t, teamID, result.Session.Selected.Team.ID)
	assert.Equal(t, APIKeyRoleManager, result.Session.Selected.Profile.Role)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, []string{"group-a"}, repo.savedCache.Groups)
	assert.Equal(t, now.Add(time.Hour), repo.savedCache.ExpiresAt)
	assert.Equal(t, "source=claims", repo.savedCache.Error)
	require.NotNil(t, repo.createdSession)
	assert.Equal(t, now.Add(time.Hour), repo.createdSession.ExpiresAt)
}

func TestSSOCompleteLoginUsesUserInfoAndGroupResolver(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	providerID := uuid.New()
	teamID := uuid.New()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	var oidcServer *httptest.Server
	oidcServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{rsaJWK(&privateKey.PublicKey, "test-key")}}))
		case "/token":
			idToken := signedOIDCToken(t, privateKey, "test-key", map[string]any{
				"iss":   oidcServer.URL,
				"sub":   "subject-123",
				"aud":   "client-id",
				"exp":   now.Add(time.Hour).Unix(),
				"iat":   now.Add(-time.Minute).Unix(),
				"nonce": "nonce-123",
			})
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"id_token":     idToken,
			}))
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"sub":   "subject-123",
				"email": "grace@example.com",
				"name":  "Grace Hopper",
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oidcServer.Close()

	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Enterprise",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: oidcServer.URL,
				ClientID:  "client-id",
				Scopes:    []string{"openid", "profile", "email"},
				Enabled:   true,
			},
		},
		consumableState: &domain.SSOOAuthState{
			StateHash:    HashSSOToken("state-token"),
			ProviderID:   providerID,
			PKCEVerifier: "pkce-verifier",
			Nonce:        "nonce-123",
			RedirectPath: "/ui",
			ExpiresAt:    now.Add(time.Minute),
		},
		mappings: []*domain.SSOGroupMapping{
			{
				ProviderID: providerID,
				TeamID:     teamID,
				TeamName:   "Enterprise Team",
				GroupID:    "group-a",
				Scopes:     []string{APIKeyScopeRead},
				Role:       APIKeyRoleMember,
				Enabled:    true,
			},
		},
	}
	resolver := &ssoGroupResolverStub{groups: []string{"group-a"}}
	svc := NewSSOService(repo, SSOConfig{
		HTTPClient:    oidcServer.Client(),
		GroupResolver: resolver,
		SessionTTL:    time.Hour,
		Now:           func() time.Time { return now },
	})

	result, err := svc.CompleteLogin(ctx, "state-token", "auth-code", "https://app.example.com/ui/api/sso/callback")

	require.NoError(t, err)
	assert.Equal(t, "grace@example.com", result.Session.Identity.Email)
	assert.Equal(t, "Grace Hopper", result.Session.Identity.DisplayName)
	assert.Equal(t, 1, resolver.calls)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, []string{"group-a"}, repo.savedCache.Groups)
}

func TestSSOProviderAndMappingManagement(t *testing.T) {
	ctx := context.Background()
	providerID := uuid.New()
	teamID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:        providerID,
				Name:      "Existing",
				Kind:      domain.SSOProviderKindGenericOIDC,
				IssuerURL: "https://issuer.example.com",
				ClientID:  "client",
				Enabled:   true,
			},
		},
	}
	repo.providerList = []*domain.SSOProvider{repo.providers[providerID]}
	svc := NewSSOService(repo, SSOConfig{})

	assert.False(t, (*SSOService)(nil).CookieSecure(ctx))
	assert.True(t, NewSSOService(repo, SSOConfig{CookieSecure: true}).CookieSecure(ctx))
	publicBaseURL, err := NewSSOService(repo, SSOConfig{PublicBaseURL: "https://portal.example.com/"}).PublicBaseURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://portal.example.com", publicBaseURL)

	runtimeErrSvc := NewSSOService(repo, SSOConfig{
		RuntimeConfig: ssoRuntimeConfigStub{err: errors.New("config unavailable")},
	})
	_, err = runtimeErrSvc.PublicBaseURL(ctx)
	require.ErrorContains(t, err, "config unavailable")

	allProviders, err := svc.ListProviders(ctx)
	require.NoError(t, err)
	require.Len(t, allProviders, 1)

	providers, err := svc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Empty(t, providers)

	publicSvc := NewSSOService(repo, SSOConfig{PublicBaseURL: "https://portal.example.com"})
	providers, err = publicSvc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	require.Len(t, providers, 1)

	created, err := svc.CreateProvider(ctx, domain.SSOProvider{
		Name:      "Azure",
		Kind:      domain.SSOProviderKindAzureAD,
		IssuerURL: "https://login.example.com/",
		ClientID:  "client-id",
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, created.ID)
	assert.Equal(t, []string{"openid", "profile", "email"}, created.Scopes)

	created.Name = "Azure Updated"
	updated, err := svc.UpdateProvider(ctx, *created)
	require.NoError(t, err)
	assert.Equal(t, "Azure Updated", updated.Name)

	err = svc.DeleteProvider(ctx, providerID)
	require.NoError(t, err)
	assert.Equal(t, providerID, repo.deletedProviderID)

	mapping, err := svc.CreateMapping(ctx, domain.SSOGroupMapping{
		ProviderID: created.ID,
		TeamID:     teamID,
		GroupID:    "group-a",
		Scopes:     []string{APIKeyScopeRead, APIKeyScopeWrite},
		Role:       APIKeyRoleManager,
	})
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, mapping.ID)
	assert.Equal(t, APIKeyRoleManager, mapping.Role)

	mapping.GroupName = "Writers"
	updatedMapping, err := svc.UpdateMapping(ctx, *mapping)
	require.NoError(t, err)
	assert.Equal(t, "Writers", updatedMapping.GroupName)

	listed, err := svc.ListMappings(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	err = svc.DeleteMapping(ctx, mapping.ID)
	require.NoError(t, err)
	assert.Equal(t, mapping.ID, repo.deletedMappingID)
}

func TestSSOServiceErrorBranches(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	profileID := uuid.New()
	sessionToken := "session-token"
	sessionHash := HashSSOToken(sessionToken)
	backendErr := errors.New("sso backend failed")
	validProvider := domain.SSOProvider{
		ID:        providerID,
		Name:      "Enterprise",
		Kind:      domain.SSOProviderKindGenericOIDC,
		IssuerURL: "https://issuer.example.com",
		ClientID:  "client-id",
		Enabled:   true,
	}
	validMapping := domain.SSOGroupMapping{
		ID:         uuid.New(),
		ProviderID: providerID,
		TeamID:     teamID,
		GroupID:    "group-a",
		Scopes:     []string{APIKeyScopeRead},
		Role:       APIKeyRoleMember,
		Enabled:    true,
	}

	var nilSvc *SSOService
	providers, err := nilSvc.ListEnabledProviders(ctx)
	require.NoError(t, err)
	assert.Empty(t, providers)
	providers, err = nilSvc.ListProviders(ctx)
	require.NoError(t, err)
	assert.Empty(t, providers)
	_, err = nilSvc.BeginLogin(ctx, providerID, "https://app.example/callback", "")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)
	_, err = nilSvc.CompleteLogin(ctx, "state", "code", "https://app.example/callback")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)
	assert.NoError(t, nilSvc.Logout(ctx, ""))

	svc := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{})
	_, err = svc.CreateProvider(ctx, domain.SSOProvider{})
	require.ErrorContains(t, err, "sso provider name is required")
	_, err = svc.UpdateProvider(ctx, domain.SSOProvider{})
	require.ErrorContains(t, err, "sso provider ID is required")
	require.ErrorContains(t, svc.DeleteProvider(ctx, uuid.Nil), "sso provider ID is required")
	_, err = svc.ListMappings(ctx, uuid.Nil)
	require.ErrorContains(t, err, "sso provider ID is required")
	_, err = svc.CreateMapping(ctx, domain.SSOGroupMapping{})
	require.ErrorContains(t, err, "sso provider ID is required")
	_, err = svc.UpdateMapping(ctx, domain.SSOGroupMapping{})
	require.ErrorContains(t, err, "sso group mapping ID is required")
	require.ErrorContains(t, svc.DeleteMapping(ctx, uuid.Nil), "sso group mapping ID is required")

	repo := &ssoRepositoryStub{t: t, listProvidersErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{PublicBaseURL: "https://portal.example.com"})
	_, err = svc.ListProviders(ctx)
	require.ErrorIs(t, err, backendErr)
	_, err = svc.ListEnabledProviders(ctx)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, createProviderErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.CreateProvider(ctx, validProvider)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, updateProviderErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.UpdateProvider(ctx, validProvider)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, getProviderErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.UpdateProvider(ctx, validProvider)
	require.ErrorIs(t, err, backendErr)
	require.NoError(t, svc.DeleteProvider(ctx, providerID))

	repo = &ssoRepositoryStub{t: t, deleteProviderErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	require.ErrorIs(t, svc.DeleteProvider(ctx, providerID), backendErr)

	repo = &ssoRepositoryStub{t: t, listMappingsErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.ListMappings(ctx, providerID)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, createMappingErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.CreateMapping(ctx, validMapping)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, updateMappingErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.UpdateMapping(ctx, validMapping)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, deleteMappingErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	require.ErrorIs(t, svc.DeleteMapping(ctx, validMapping.ID), backendErr)

	repo = &ssoRepositoryStub{t: t}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.BeginLogin(ctx, providerID, "https://app.example/callback", "")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	repo = &ssoRepositoryStub{t: t, providers: map[uuid.UUID]*domain.SSOProvider{
		providerID: {ID: providerID, Name: "Enterprise", Enabled: false},
	}}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.BeginLogin(ctx, providerID, "https://app.example/callback", "")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	badOIDCProvider := validProvider
	badOIDCProvider.IssuerURL = "https://127.0.0.1:1"
	repo = &ssoRepositoryStub{t: t, providers: map[uuid.UUID]*domain.SSOProvider{providerID: &badOIDCProvider}}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.BeginLogin(ctx, providerID, "https://app.example/callback", "")
	require.Error(t, err)

	discovery := ssoDiscoveryServer(t)
	defer discovery.Close()
	discoveredProvider := validProvider
	discoveredProvider.IssuerURL = discovery.URL
	repo = &ssoRepositoryStub{
		t:                   t,
		providers:           map[uuid.UUID]*domain.SSOProvider{providerID: &discoveredProvider},
		createOAuthStateErr: backendErr,
	}
	svc = NewSSOService(repo, SSOConfig{HTTPClient: discovery.Client(), Now: func() time.Time { return now }})
	_, err = svc.BeginLogin(ctx, providerID, "https://app.example/callback", "")
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, consumeOAuthStateErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.CompleteLogin(ctx, "state", "code", "https://app.example/callback")
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.CompleteLogin(ctx, "state", "code", "https://app.example/callback")
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	repo = &ssoRepositoryStub{
		t:               t,
		consumableState: &domain.SSOOAuthState{StateHash: HashSSOToken("state"), ProviderID: providerID},
	}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.CompleteLogin(ctx, "state", "code", "https://app.example/callback")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	repo = &ssoRepositoryStub{
		t:               t,
		providers:       map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: false}},
		consumableState: &domain.SSOOAuthState{StateHash: HashSSOToken("state"), ProviderID: providerID},
	}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.CompleteLogin(ctx, "state", "code", "https://app.example/callback")
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	repo = &ssoRepositoryStub{t: t, getSessionErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	repo = &ssoRepositoryStub{
		t: t,
		sessions: map[string]*domain.SSOSession{
			sessionHash: {SessionHash: sessionHash, TeamProfileID: profileID, ExpiresAt: now.Add(time.Hour)},
		},
	}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	_, err = svc.AuthenticateSession(ctx, sessionToken, "", false)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	repo = &ssoRepositoryStub{
		t: t,
		sessions: map[string]*domain.SSOSession{
			sessionHash: {SessionHash: sessionHash, IdentityID: uuid.New(), TeamProfileID: profileID, ExpiresAt: now.Add(-time.Minute)},
		},
	}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	_, err = svc.CurrentSession(ctx, sessionToken)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	identityID := uuid.New()
	repo = &ssoRepositoryStub{
		t: t,
		sessions: map[string]*domain.SSOSession{
			sessionHash: {SessionHash: sessionHash, IdentityID: identityID, TeamProfileID: profileID, ExpiresAt: now.Add(time.Hour)},
		},
	}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	_, err = svc.CurrentSession(ctx, sessionToken)
	require.ErrorIs(t, err, ErrSSOSessionInvalid)

	repo = &ssoRepositoryStub{
		t: t,
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {ID: identityID, ProviderID: providerID, Subject: "subject"},
		},
		sessions: map[string]*domain.SSOSession{
			sessionHash: {SessionHash: sessionHash, IdentityID: identityID, TeamProfileID: profileID, ExpiresAt: now.Add(time.Hour)},
		},
	}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	_, err = svc.CurrentSession(ctx, sessionToken)
	require.ErrorIs(t, err, ErrSSOAccessDenied)
	_, err = svc.SwitchSessionTeam(ctx, sessionToken, profileID)
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	_, err = svc.ValidateAPIKeyPrincipal(ctx, nil)
	require.ErrorIs(t, err, ErrSSOAccessDenied)
	keyWithoutSSO := &domain.APIKey{ID: uuid.New(), TeamID: teamID}
	validated, err := svc.ValidateAPIKeyPrincipal(ctx, keyWithoutSSO)
	require.NoError(t, err)
	assert.Same(t, keyWithoutSSO, validated)

	repo = &ssoRepositoryStub{t: t, getProviderErr: backendErr}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.ValidateAPIKeyPrincipal(ctx, &domain.APIKey{ID: uuid.New(), TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"})
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{t: t, providers: map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: false}}}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.ValidateAPIKeyPrincipal(ctx, &domain.APIKey{ID: uuid.New(), TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"})
	require.ErrorIs(t, err, ErrSSOProviderDisabled)

	repo = &ssoRepositoryStub{
		t:         t,
		providers: map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: true}},
		cacheErr:  backendErr,
	}
	svc = NewSSOService(repo, SSOConfig{})
	_, err = svc.ValidateAPIKeyPrincipal(ctx, &domain.APIKey{ID: uuid.New(), TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"})
	require.ErrorIs(t, err, backendErr)

	repo = &ssoRepositoryStub{
		t:         t,
		providers: map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: true}},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject",
			Status:     "denied",
			ExpiresAt:  now.Add(time.Hour),
		},
	}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	_, err = svc.ValidateAPIKeyPrincipal(ctx, &domain.APIKey{ID: uuid.New(), TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"})
	require.ErrorIs(t, err, ErrSSOAccessDenied)

	repo = &ssoRepositoryStub{
		t:                    t,
		providers:            map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: true}},
		mappingsForGroupsErr: backendErr,
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject",
			Groups:     []string{"group-a"},
			Status:     "active",
			ExpiresAt:  now.Add(time.Hour),
		},
	}
	svc = NewSSOService(repo, SSOConfig{Now: func() time.Time { return now }})
	_, err = svc.ValidateAPIKeyPrincipal(ctx, &domain.APIKey{ID: uuid.New(), TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"})
	require.ErrorIs(t, err, backendErr)
}

func ssoDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 server.URL,
			"authorization_endpoint": server.URL + "/authorize",
			"token_endpoint":         server.URL + "/token",
			"jwks_uri":               server.URL + "/jwks",
			"userinfo_endpoint":      server.URL + "/userinfo",
		}))
	}))
	return server
}

func signedOIDCToken(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func rsaJWK(key *rsa.PublicKey, keyID string) map[string]string {
	exponent := big.NewInt(int64(key.E)).Bytes()
	return map[string]string{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": keyID,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(exponent),
	}
}

func TestSSOSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	teamAID := uuid.New()
	teamBID := uuid.New()
	profileAID := uuid.New()
	profileBID := uuid.New()
	sessionToken := "session-token"
	csrfToken := "csrf-token"
	sessionHash := HashSSOToken(sessionToken)

	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {ID: providerID, Name: "Enterprise", Enabled: true},
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject-123",
			Groups:     []string{"group-a", "group-b"},
			Status:     "active",
			CheckedAt:  now.Add(-time.Minute),
			ExpiresAt:  now.Add(time.Minute),
		},
		mappings: []*domain.SSOGroupMapping{
			{ProviderID: providerID, TeamID: teamAID, GroupID: "group-a", Scopes: []string{APIKeyScopeRead}, Role: APIKeyRoleMember, Enabled: true},
			{ProviderID: providerID, TeamID: teamBID, GroupID: "group-b", Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}, Role: APIKeyRoleManager, Enabled: true},
		},
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {ID: identityID, ProviderID: providerID, Subject: "subject-123", Email: "ada@example.com"},
		},
		sessions: map[string]*domain.SSOSession{
			sessionHash: {
				SessionHash:   sessionHash,
				IdentityID:    identityID,
				ProviderID:    providerID,
				TeamProfileID: profileAID,
				TeamID:        teamAID,
				CSRFHash:      HashSSOToken(csrfToken),
				ExpiresAt:     now.Add(time.Hour),
			},
		},
	}
	repo.teamProfiles = []*domain.SSOTeamProfile{
		ssoTeamProfile(identityID, providerID, "subject-123", teamAID, profileAID, "Team A", []string{APIKeyScopeRead}, APIKeyRoleMember),
		ssoTeamProfile(identityID, providerID, "subject-123", teamBID, profileBID, "Team B", []string{APIKeyScopeRead, APIKeyScopeWrite}, APIKeyRoleManager),
	}
	repo.ssoProfiles = map[uuid.UUID]*domain.APIKey{
		profileAID: &repo.teamProfiles[0].Profile,
		profileBID: &repo.teamProfiles[1].Profile,
	}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver: &ssoGroupResolverStub{t: t, unexpected: true},
		Now:           func() time.Time { return now },
	})

	key, err := svc.AuthenticateSession(ctx, sessionToken, csrfToken, true)
	require.NoError(t, err)
	assert.Equal(t, profileAID, key.ID)
	assert.Equal(t, []string{APIKeyScopeRead}, key.Scopes)

	_, err = svc.AuthenticateSession(ctx, sessionToken, "wrong", true)
	require.ErrorIs(t, err, ErrSSOCSRFInvalid)

	info, err := svc.CurrentSession(ctx, sessionToken)
	require.NoError(t, err)
	assert.Equal(t, profileAID, info.Selected.Profile.ID)
	assert.Len(t, info.Teams, 2)

	switched, err := svc.SwitchSessionTeam(ctx, sessionToken, profileBID)
	require.NoError(t, err)
	assert.Equal(t, profileBID, switched.Selected.Profile.ID)
	assert.Equal(t, sessionHash, repo.updatedSessionHash)
	assert.Equal(t, profileBID, repo.sessions[sessionHash].TeamProfileID)

	err = svc.Logout(ctx, sessionToken)
	require.NoError(t, err)
	assert.Equal(t, sessionHash, repo.deletedSessionHash)
	assert.Empty(t, repo.sessions)
}

func TestSSOEntitlementsFromMappings(t *testing.T) {
	providerID := uuid.New()
	teamAID := uuid.New()
	teamBID := uuid.New()
	svc := NewSSOService(&ssoRepositoryStub{t: t}, SSOConfig{})

	entitlements, err := svc.entitlementsFromMappings(providerID, "subject", []*domain.SSOGroupMapping{
		{ProviderID: providerID, TeamID: teamAID, TeamName: "Beta", GroupID: "g1", Scopes: []string{APIKeyScopeRead}, Role: APIKeyRoleMember, Enabled: true},
		{ProviderID: providerID, TeamID: teamAID, TeamName: "Beta", GroupID: "g2", Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}, Role: APIKeyRoleManager, Enabled: true},
		{ProviderID: providerID, TeamID: teamBID, TeamName: "Alpha", GroupID: "g3", Role: APIKeyRoleMember, Enabled: true},
		{ProviderID: uuid.New(), TeamID: uuid.New(), GroupID: "ignored", Enabled: true},
	})

	require.NoError(t, err)
	require.Len(t, entitlements, 2)
	assert.Equal(t, "Alpha", entitlements[0].TeamName)
	assert.Equal(t, []string{APIKeyScopeRead}, entitlements[0].Scopes)
	assert.Equal(t, "g1,g2", entitlements[1].GroupID)
	assert.Equal(t, APIKeyRoleManager, entitlements[1].Role)

	_, err = svc.entitlementsFromMappings(providerID, "subject", nil)
	require.ErrorIs(t, err, ErrSSOAccessDenied)
}

func TestSSOCurrentEntitledTeamsFiltersAllowedProfiles(t *testing.T) {
	identityID := uuid.New()
	providerID := uuid.New()
	teamAID := uuid.New()
	teamBID := uuid.New()
	profileAID := uuid.New()
	profileBID := uuid.New()
	repo := &ssoRepositoryStub{t: t}
	repo.teamProfiles = []*domain.SSOTeamProfile{
		ssoTeamProfile(identityID, providerID, "subject", teamAID, profileAID, "Team A", []string{APIKeyScopeRead}, APIKeyRoleMember),
		ssoTeamProfile(identityID, providerID, "subject", teamBID, profileBID, "Team B", []string{APIKeyScopeRead}, APIKeyRoleMember),
	}
	svc := NewSSOService(repo, SSOConfig{})

	teams, err := svc.currentEntitledTeams(context.Background(), identityID, map[uuid.UUID]struct{}{profileBID: {}})

	require.NoError(t, err)
	require.Len(t, teams, 1)
	assert.Equal(t, profileBID, teams[0].Profile.ID)
}

func TestSSOValidateAPIKeyPrincipalRefreshesCache(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {ID: providerID, Name: "Enterprise", Enabled: true},
		},
		mappings: []*domain.SSOGroupMapping{
			{ProviderID: providerID, TeamID: teamID, GroupID: "group-a", Scopes: []string{APIKeyScopeRead, APIKeyScopeWrite}, Role: APIKeyRoleManager, Enabled: true},
		},
	}
	resolver := &ssoGroupResolverStub{groups: []string{"group-a"}}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver:       resolver,
		EntitlementCacheTTL: time.Minute,
		Now:                 func() time.Time { return now },
	})
	key := &domain.APIKey{ID: uuid.New(), ProfileID: teamID, TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"}

	validated, err := svc.ValidateAPIKeyPrincipal(context.Background(), key)

	require.NoError(t, err)
	assert.Equal(t, []string{APIKeyScopeRead, APIKeyScopeWrite}, validated.Scopes)
	assert.Equal(t, APIKeyRoleManager, validated.Role)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, "active", repo.savedCache.Status)
	assert.Equal(t, 1, resolver.calls)
}

func TestSSOValidateAPIKeyPrincipalRefreshDeniesWhenNoTeamMapping(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	repo := &ssoRepositoryStub{
		t: t,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {ID: providerID, Name: "Enterprise", Enabled: true},
		},
	}
	svc := NewSSOService(repo, SSOConfig{
		GroupResolver:       &ssoGroupResolverStub{groups: []string{"group-a"}},
		EntitlementCacheTTL: time.Minute,
		Now:                 func() time.Time { return now },
	})
	key := &domain.APIKey{ID: uuid.New(), ProfileID: teamID, TeamID: teamID, SSOProviderID: &providerID, SSOSubject: "subject"}

	validated, err := svc.ValidateAPIKeyPrincipal(context.Background(), key)

	require.ErrorIs(t, err, ErrSSOAccessDenied)
	assert.Nil(t, validated)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, "denied", repo.savedCache.Status)
}

func ssoTeamProfile(identityID, providerID uuid.UUID, subject string, teamID, profileID uuid.UUID, teamName string, scopes []string, role string) *domain.SSOTeamProfile {
	return &domain.SSOTeamProfile{
		Team: domain.Profile{
			ID:   teamID,
			Name: teamName,
		},
		Profile: domain.APIKey{
			ID:            profileID,
			ProfileID:     teamID,
			TeamID:        teamID,
			TeamName:      teamName,
			Name:          "SSO profile",
			Scopes:        scopes,
			Role:          role,
			RateLimit:     120,
			SSOIdentityID: &identityID,
			SSOProviderID: &providerID,
			SSOSubject:    subject,
		},
	}
}
