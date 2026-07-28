package http

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestSSOOIDCCallbackSkipsArchivedTeamMappingIntegration(t *testing.T) {
	db := setupSSOHTTPIntegrationDB(t)
	ctx := context.Background()
	rls := storagepostgres.NewRLS()
	ssoRepo := repository.NewSSORepository(db, rls)
	idp := newSSOIntegrationOIDCProvider(t)

	activeTeamID := uuid.New()
	archivedTeamID := uuid.New()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES
			    (?::uuid, 'Active SSO Team', '', '{}'::jsonb, '{}'::jsonb),
			    (?::uuid, 'Archived SSO Team', '', '{}'::jsonb, '{}'::jsonb)
		`, activeTeamID, archivedTeamID).Error
	}))

	runtime := &ssoIntegrationRuntimeConfig{}
	ssoSvc := service.NewSSOService(ssoRepo, service.SSOConfig{
		HTTPClient:    idp.server.Client(),
		RuntimeConfig: runtime,
	})
	provider, err := ssoSvc.CreateProvider(ctx, domain.SSOProvider{
		Name:        "Integration OIDC",
		Kind:        domain.SSOProviderKindGenericOIDC,
		IssuerURL:   idp.server.URL,
		ClientID:    "dense-mem-integration",
		Scopes:      []string{"openid", "profile", "email"},
		GroupClaims: []string{"groups"},
		Enabled:     true,
	})
	require.NoError(t, err)
	for _, mapping := range []domain.SSOGroupMapping{
		{
			ProviderID: provider.ID,
			TeamID:     archivedTeamID,
			GroupID:    "engineering",
			Scopes:     []string{service.APIKeyScopeRead},
			Role:       service.APIKeyRoleMember,
			Enabled:    true,
		},
		{
			ProviderID: provider.ID,
			TeamID:     activeTeamID,
			GroupID:    "engineering",
			Scopes:     []string{service.APIKeyScopeRead, service.APIKeyScopeWrite},
			Role:       service.APIKeyRoleManager,
			Enabled:    true,
		},
	} {
		_, err := ssoSvc.CreateMapping(ctx, mapping)
		require.NoError(t, err)
	}
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE teams
			SET status = 'archived',
			    deleted_at = now()
			WHERE id = ?::uuid
		`, archivedTeamID).Error
	}))

	cfg := &config.Config{
		RateLimitPerMinute:      100,
		FragmentCreateRateLimit: 60,
		FragmentReadRateLimit:   300,
		ClaimWriteRateLimit:     60,
		ClaimReadRateLimit:      300,
	}
	e := NewServer(*cfg, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:    repository.NewAPIKeyRepository(db, rls),
		RateLimitSvc:  service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		SSOService:    ssoSvc,
		Config:        cfg,
		UserStaticDir: t.TempDir(),
	})
	app := httptest.NewServer(e)
	t.Cleanup(app.Close)
	runtime.setPublicBaseURL(app.URL)

	client := &nethttp.Client{
		CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error {
			return nethttp.ErrUseLastResponse
		},
	}
	startURL := app.URL + "/ui/api/sso/start/" + provider.ID.String() + "?redirect=" + url.QueryEscape("/ui/knowledge")
	startResponse, err := client.Get(startURL)
	require.NoError(t, err)
	authorizationURL := integrationRedirectLocation(t, startResponse, nethttp.StatusFound)
	parsedAuthorizationURL, err := url.Parse(authorizationURL)
	require.NoError(t, err)
	require.Equal(t, idp.server.URL+"/authorize", parsedAuthorizationURL.Scheme+"://"+parsedAuthorizationURL.Host+parsedAuthorizationURL.Path)
	require.NotEmpty(t, parsedAuthorizationURL.Query().Get("state"))
	require.NotEmpty(t, parsedAuthorizationURL.Query().Get("nonce"))
	idp.setNonce(parsedAuthorizationURL.Query().Get("nonce"))

	callbackURL := app.URL + "/ui/api/sso/callback?code=integration-code&state=" +
		url.QueryEscape(parsedAuthorizationURL.Query().Get("state"))
	callbackResponse, err := client.Get(callbackURL)
	require.NoError(t, err)
	cookies := callbackResponse.Cookies()
	require.Equal(t, "/ui/knowledge", integrationRedirectLocation(t, callbackResponse, nethttp.StatusFound))
	require.NotEmpty(t, integrationCookieValue(cookies, service.SSOSessionCookieName))

	sessionRequest, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, app.URL+"/ui/api/session", nil)
	require.NoError(t, err)
	for _, cookie := range cookies {
		sessionRequest.AddCookie(cookie)
	}
	sessionResponse, err := client.Do(sessionRequest)
	require.NoError(t, err)
	defer sessionResponse.Body.Close()
	require.Equal(t, nethttp.StatusOK, sessionResponse.StatusCode)
	var session struct {
		Data struct {
			Team struct {
				ID uuid.UUID `json:"id"`
			} `json:"team"`
			Key struct {
				TeamID uuid.UUID `json:"team_id"`
				Role   string    `json:"role"`
			} `json:"key"`
			Teams      []json.RawMessage `json:"teams"`
			AuthMethod string            `json:"auth_method"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(sessionResponse.Body).Decode(&session))
	assert.Equal(t, "sso", session.Data.AuthMethod)
	assert.Equal(t, activeTeamID, session.Data.Team.ID)
	assert.Equal(t, activeTeamID, session.Data.Key.TeamID)
	assert.Equal(t, service.APIKeyRoleManager, session.Data.Key.Role)
	assert.Len(t, session.Data.Teams, 1)

	identity, err := ssoRepo.GetIdentityByProviderSubject(ctx, provider.ID, "integration-subject")
	require.NoError(t, err)
	require.NotNil(t, identity)
	profiles, err := ssoRepo.ListTeamProfilesForIdentity(ctx, identity.ID)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, activeTeamID, profiles[0].Team.ID)
}

func setupSSOHTTPIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 to run disposable SSO HTTP integration tests")
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx,
		"pgvector/pgvector:0.8.2-pg18-trixie",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storagepostgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			require.NoError(t, sqlDB.Close())
		}
	})
	return db
}

type ssoIntegrationRuntimeConfig struct {
	mu            sync.RWMutex
	publicBaseURL string
}

func (c *ssoIntegrationRuntimeConfig) SSORuntimeConfig(context.Context) (service.SSORuntimeConfig, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return service.SSORuntimeConfig{
		PublicBaseURL:       c.publicBaseURL,
		EntitlementCacheTTL: service.DefaultSSOEntitlementCacheTTL,
		SessionTTL:          service.DefaultSSOSessionTTL,
		StateTTL:            service.DefaultSSOStateTTL,
		HTTPTimeout:         service.DefaultSSOHTTPTimeout,
	}, nil
}

func (c *ssoIntegrationRuntimeConfig) setPublicBaseURL(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publicBaseURL = value
}

type ssoIntegrationOIDCProvider struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	mu         sync.RWMutex
	nonce      string
}

func newSSOIntegrationOIDCProvider(t *testing.T) *ssoIntegrationOIDCProvider {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	provider := &ssoIntegrationOIDCProvider{privateKey: privateKey}
	provider.server = httptest.NewServer(nethttp.HandlerFunc(provider.handle))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *ssoIntegrationOIDCProvider) setNonce(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce = value
}

func (p *ssoIntegrationOIDCProvider) currentNonce() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.nonce
}

func (p *ssoIntegrationOIDCProvider) handle(w nethttp.ResponseWriter, r *nethttp.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		integrationWriteJSON(w, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"userinfo_endpoint":                     p.server.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/jwks":
		integrationWriteJSON(w, map[string]any{
			"keys": []map[string]string{integrationRSAJWK(&p.privateKey.PublicKey, "integration-key")},
		})
	case "/token":
		now := time.Now().UTC()
		idToken, err := integrationSignedOIDCToken(p.privateKey, "integration-key", map[string]any{
			"iss":    p.server.URL,
			"sub":    "integration-subject",
			"aud":    "dense-mem-integration",
			"exp":    now.Add(time.Hour).Unix(),
			"iat":    now.Add(-time.Minute).Unix(),
			"nonce":  p.currentNonce(),
			"email":  "integration@example.com",
			"name":   "Integration User",
			"groups": []string{"engineering"},
		})
		if err != nil {
			nethttp.Error(w, "failed to sign ID token", nethttp.StatusInternalServerError)
			return
		}
		integrationWriteJSON(w, map[string]any{
			"access_token": "integration-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	case "/userinfo":
		integrationWriteJSON(w, map[string]any{"sub": "integration-subject"})
	default:
		nethttp.NotFound(w, r)
	}
}

func integrationSignedOIDCToken(key *rsa.PrivateKey, keyID string, claims map[string]any) (string, error) {
	headerJSON, err := json.Marshal(map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func integrationRSAJWK(key *rsa.PublicKey, keyID string) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": keyID,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func integrationWriteJSON(w nethttp.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		nethttp.Error(w, "failed to encode response", nethttp.StatusInternalServerError)
	}
}

func integrationRedirectLocation(t *testing.T, response *nethttp.Response, status int) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equalf(t, status, response.StatusCode, "body: %s", body)
	location := response.Header.Get("Location")
	require.NotEmpty(t, location)
	return location
}

func integrationCookieValue(cookies []*nethttp.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
