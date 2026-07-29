package service

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
				Scopes:     []string{APIKeyScopeRead},
				Role:       APIKeyRoleMember,
				Enabled:    true,
			},
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
	assert.Equal(t, APIKeyRoleManager, result.Session.Selected.Profile.Role)
	require.NotNil(t, repo.savedCache)
	assert.Equal(t, []string{"group-a"}, repo.savedCache.Groups)
	assert.Equal(t, now.Add(time.Hour), repo.savedCache.ExpiresAt)
	assert.Equal(t, "source=claims", repo.savedCache.Error)
	require.NotNil(t, repo.createdSession)
	assert.Equal(t, now.Add(time.Hour), repo.createdSession.ExpiresAt)
}
