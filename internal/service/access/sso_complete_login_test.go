package access

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSSOCompleteLoginCreatesSession(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	providerID := uuid.New()
	archivedTeamID := uuid.New()
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
				TeamID:     archivedTeamID,
				TeamName:   "Archived Team",
				GroupID:    "group-a",
				Scopes:     []string{CredentialScopeRead},
				Role:       CredentialRoleMember,
				Enabled:    true,
			},
			{
				ProviderID: providerID,
				TeamID:     teamID,
				TeamName:   "Enterprise Team",
				GroupID:    "group-a",
				Scopes:     []string{CredentialScopeRead, CredentialScopeWrite},
				Role:       CredentialRoleManager,
				Enabled:    true,
			},
		},
		upsertProfileErrors: map[uuid.UUID]error{archivedTeamID: repository.ErrTeamInactive},
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
	assert.Equal(t, CredentialRoleManager, result.Session.Selected.Membership.Role)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, []string{"group-a"}, repo.savedCache.Groups)
	assert.Equal(t, now.Add(time.Hour), repo.savedCache.ExpiresAt)
	assert.Equal(t, "source=claims", repo.savedCache.Error)
	require.NotNil(t, repo.createdSession)
	assert.Equal(t, now.Add(time.Hour), repo.createdSession.ExpiresAt)
}

func TestSSOCompleteLoginUsesDurableDirectoryProfilesWhenDirectoryAuthorityIsActive(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	providerID := uuid.New()
	identityID := uuid.New()
	directoryTeamID := uuid.New()
	claimOnlyTeamID := uuid.New()
	directoryProfileID := uuid.New()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tokenTenantID := "tenant-id"

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
				"sub":   "entra-user-1",
				"oid":   "entra-user-1",
				"tid":   tokenTenantID,
				"aud":   "client-id",
				"exp":   now.Add(time.Hour).Unix(),
				"iat":   now.Add(-time.Minute).Unix(),
				"nonce": "nonce-123",
				"email": "alex@example.test",
				"name":  "Alex Entra",
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
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"sub": "entra-user-1"}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer oidcServer.Close()

	repo := &ssoRepositoryStub{
		t:                        t,
		directoryAuthorityActive: true,
		upsertIdentityID:         identityID,
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: {
				ID:             providerID,
				Name:           "Directory authority",
				Kind:           domain.SSOProviderKindAzureAD,
				IssuerURL:      oidcServer.URL,
				TenantID:       "tenant-id",
				ClientID:       "client-id",
				GroupsEndpoint: "https://graph.example.test/groups",
				Enabled:        true,
			},
		},
		consumableState: &domain.SSOOAuthState{
			StateHash:    HashSSOToken("state-token"),
			ProviderID:   providerID,
			PKCEVerifier: "pkce-verifier",
			Nonce:        "nonce-123",
			RedirectPath: "/ui/knowledge",
			ExpiresAt:    now.Add(time.Minute),
		},
		mappings: []*domain.SSOGroupMapping{{
			ProviderID: providerID,
			TeamID:     claimOnlyTeamID,
			TeamName:   "Claim Only",
			GroupID:    "entra-claim-only-manager",
			Scopes:     []string{CredentialScopeRead, CredentialScopeWrite},
			Role:       CredentialRoleManager,
			Enabled:    true,
		}},
		teamProfiles: []*domain.SSOTeamMembership{{
			Team: domain.Team{ID: directoryTeamID, Name: "Research"},
			Membership: domain.Membership{
				ID:              directoryProfileID,
				ActorIdentityID: identityID,
				TeamID:          directoryTeamID,
				OwnerID:         directoryProfileID,
				SSOProviderID:   &providerID,
				SSOSubject:      "entra-user-1",
				SSOGroupID:      "entra-research-manager",
				Grants:          []string{CredentialScopeRead, CredentialScopeWrite},
				Role:            CredentialRoleManager,
				Status:          "active",
			},
		}},
		directoryProfileEntitled: map[uuid.UUID]bool{identityID: true},
	}
	resolver := &ssoGroupResolverStub{groups: []string{"entra-claim-only-manager"}}
	svc := NewSSOService(repo, SSOConfig{
		HTTPClient:    oidcServer.Client(),
		GroupResolver: resolver,
		SessionTTL:    time.Hour,
		Now:           func() time.Time { return now },
	})

	result, err := svc.CompleteLogin(ctx, "state-token", "auth-code", "https://app.example.com/ui/api/sso/callback")

	require.NoError(t, err)
	require.Len(t, result.Session.Teams, 1)
	assert.Equal(t, directoryTeamID, result.Session.Selected.Team.ID)
	assert.Equal(t, directoryTeamID, result.Session.Teams[0].Team.ID)
	assert.Zero(t, resolver.calls)
	assert.Zero(t, repo.mappingLookupCalls)
	assert.Zero(t, repo.upsertProfileCalls)
	assert.Nil(t, repo.savedCache)
	assert.Equal(t, []uuid.UUID{identityID}, repo.directoryProfileEntitledCalls)

	repo.providers[providerID].TenantID = ""
	repo.consumableState = &domain.SSOOAuthState{
		StateHash:    HashSSOToken("legacy-state-token"),
		ProviderID:   providerID,
		PKCEVerifier: "pkce-verifier",
		Nonce:        "nonce-123",
		RedirectPath: "/ui/knowledge",
		ExpiresAt:    now.Add(time.Minute),
	}
	legacyResult, err := svc.CompleteLogin(ctx, "legacy-state-token", "auth-code", "https://app.example.com/ui/api/sso/callback")
	require.NoError(t, err)
	require.Len(t, legacyResult.Session.Teams, 1)
	assert.Equal(t, directoryTeamID, legacyResult.Session.Selected.Team.ID)

	repo.providers[providerID].TenantID = "tenant-id"
	tokenTenantID = "other-tenant"
	repo.consumableState = &domain.SSOOAuthState{
		StateHash:    HashSSOToken("wrong-tenant-state-token"),
		ProviderID:   providerID,
		PKCEVerifier: "pkce-verifier",
		Nonce:        "nonce-123",
		RedirectPath: "/ui/knowledge",
		ExpiresAt:    now.Add(time.Minute),
	}
	_, err = svc.CompleteLogin(ctx, "wrong-tenant-state-token", "auth-code", "https://app.example.com/ui/api/sso/callback")
	require.ErrorContains(t, err, "tenant")
}
