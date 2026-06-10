//go:build uat

package uat

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	httpserver "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	pgclient "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	uatOIDCClientID = "dense-mem-uat-client"
	uatOIDCKeyID    = "uat-oidc-key"
	uatSSOSubject   = "subject-uat-123"
	uatSSOEmail     = "sso-user@example.com"
	uatSSOName      = "SSO User"
	uatSSOGroupID   = "engineering"
)

func TestUATSSOOIDCLoginCreatesPortalSession(t *testing.T) {
	ctx := context.Background()
	env, cleanup := SetupTestEnv(t, ctx, TestEnvOptions{NoRedisMode: true})
	defer cleanup()

	idp := newUATMockOIDCProvider(t)

	rls := pgclient.NewRLS()
	ssoRepo := repository.NewSSORepository(env.db, rls)
	appConfigSvc := service.NewAppConfigService(repository.NewAppConfigRepository(env.db, rls), env.auditService)
	ssoSvc := service.NewSSOService(ssoRepo, service.SSOConfig{
		RuntimeConfig: appConfigSvc,
		HTTPClient:    idp.Client(),
	})

	team, err := env.profileSvc.Create(ctx, service.CreateProfileRequest{
		Name:        "SSO UAT Team",
		Description: "Team provisioned through SSO UAT",
	}, nil, "system", "127.0.0.1", "uat-sso-oidc")
	require.NoError(t, err)

	provider, err := ssoSvc.CreateProvider(ctx, domain.SSOProvider{
		Name:        "UAT OIDC",
		Kind:        domain.SSOProviderKindGenericOIDC,
		IssuerURL:   idp.URL(),
		ClientID:    uatOIDCClientID,
		Scopes:      []string{"openid", "profile", "email"},
		GroupClaims: []string{"groups"},
		Enabled:     true,
	})
	require.NoError(t, err)

	_, err = ssoSvc.CreateMapping(ctx, domain.SSOGroupMapping{
		ProviderID: provider.ID,
		TeamID:     team.ID,
		GroupID:    uatSSOGroupID,
		GroupName:  "Engineering",
		Scopes:     []string{service.APIKeyScopeRead, service.APIKeyScopeWrite},
		Role:       service.APIKeyRoleManager,
		Enabled:    true,
	})
	require.NoError(t, err)

	app := startUATSSOPortalServer(t, env, ssoSvc)
	_, err = appConfigSvc.UpdateSSOSettings(ctx, map[string]string{
		domain.AppConfigSSOPublicBaseURL: app.URL,
		domain.AppConfigSSOCookieSecure:  "false",
	}, "system", "127.0.0.1", "uat-sso-oidc")
	require.NoError(t, err)

	client := &nethttp.Client{
		CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error {
			return nethttp.ErrUseLastResponse
		},
	}

	var providers uatSSOProvidersResponse
	resp := doUATGET(t, client, app.URL+"/ui/api/sso/providers")
	decodeUATJSONResponse(t, resp, nethttp.StatusOK, &providers)
	require.Len(t, providers.Data, 1)
	require.Equal(t, provider.ID, providers.Data[0].ID)
	require.Equal(t, "UAT OIDC", providers.Data[0].Name)
	require.Equal(t, string(domain.SSOProviderKindGenericOIDC), providers.Data[0].Kind)

	startURL := app.URL + "/ui/api/sso/start/" + provider.ID.String() + "?redirect=" + url.QueryEscape("/ui/knowledge")
	resp = doUATGET(t, client, startURL)
	idpAuthorizeURL := requireUATRedirect(t, resp, nethttp.StatusFound)
	parsedAuthorizeURL, err := url.Parse(idpAuthorizeURL)
	require.NoError(t, err)
	require.Equal(t, idp.URL(), parsedAuthorizeURL.Scheme+"://"+parsedAuthorizeURL.Host)
	require.Equal(t, "/authorize", parsedAuthorizeURL.Path)
	require.Equal(t, uatOIDCClientID, parsedAuthorizeURL.Query().Get("client_id"))
	require.Equal(t, app.URL+"/ui/api/sso/callback", parsedAuthorizeURL.Query().Get("redirect_uri"))
	require.NotEmpty(t, parsedAuthorizeURL.Query().Get("state"))
	require.NotEmpty(t, parsedAuthorizeURL.Query().Get("nonce"))
	require.NotEmpty(t, parsedAuthorizeURL.Query().Get("code_challenge"))

	resp = doUATGET(t, client, idpAuthorizeURL)
	callbackURL := requireUATRedirect(t, resp, nethttp.StatusFound)
	parsedCallbackURL, err := url.Parse(callbackURL)
	require.NoError(t, err)
	require.Equal(t, app.URL, parsedCallbackURL.Scheme+"://"+parsedCallbackURL.Host)
	require.Equal(t, "/ui/api/sso/callback", parsedCallbackURL.Path)
	require.NotEmpty(t, parsedCallbackURL.Query().Get("code"))
	require.Equal(t, parsedAuthorizeURL.Query().Get("state"), parsedCallbackURL.Query().Get("state"))

	resp = doUATGET(t, client, callbackURL)
	cookies := resp.Cookies()
	finalRedirect := requireUATRedirect(t, resp, nethttp.StatusFound)
	require.Equal(t, "/ui/knowledge", finalRedirect)
	require.NotEmpty(t, findUATCookie(cookies, service.SSOSessionCookieName))
	require.NotEmpty(t, findUATCookie(cookies, service.SSOCSRFCookieName))

	var session uatSSOSessionResponse
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, app.URL+"/ui/api/session", nil)
	require.NoError(t, err)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err = client.Do(req)
	require.NoError(t, err)
	decodeUATJSONResponse(t, resp, nethttp.StatusOK, &session)
	require.Equal(t, "sso", session.Data.AuthMethod)
	require.Equal(t, team.ID, session.Data.Team.ID)
	require.Equal(t, team.ID, session.Data.Key.TeamID)
	require.Equal(t, service.APIKeyRoleManager, session.Data.Key.Role)
	require.ElementsMatch(t, []string{service.APIKeyScopeRead, service.APIKeyScopeWrite}, session.Data.Key.Scopes)
	require.True(t, session.Data.CanRotate)
	require.True(t, session.Data.CanManageTeam)
	require.Len(t, session.Data.Teams, 1)

	identity, err := ssoRepo.GetIdentityByProviderSubject(ctx, provider.ID, uatSSOSubject)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, uatSSOEmail, identity.Email)
	require.Equal(t, uatSSOName, identity.DisplayName)

	cache, err := ssoRepo.GetEntitlementCache(ctx, provider.ID, uatSSOSubject)
	require.NoError(t, err)
	require.NotNil(t, cache)
	require.Equal(t, "active", cache.Status)
	require.ElementsMatch(t, []string{uatSSOGroupID}, cache.Groups)
}

func startUATSSOPortalServer(t *testing.T, env *TestEnv, ssoSvc *service.SSOService) *httptest.Server {
	t.Helper()

	logger := observability.New(slog.LevelError)
	server := httpserver.NewServer(env.buildConfigConcrete(), logger, httpserver.HealthConfig{})
	httpserver.RegisterUserPortal(server, httpserver.UserPortalDeps{
		APIKeyRepo:    env.apiKeyRepo,
		ProfileSvc:    env.profileSvc,
		APIKeySvc:     env.apiKeySvc,
		RateLimitSvc:  env.rateLimitSvc,
		AuditSvc:      env.auditService,
		SSOService:    ssoSvc,
		Config:        env.buildConfig(),
		UserStaticDir: "testdata/missing-user-portal",
	})

	app := httptest.NewServer(server)
	t.Cleanup(app.Close)
	return app
}

type uatSSOProvidersResponse struct {
	Data []struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
		Kind string    `json:"kind"`
	} `json:"data"`
}

type uatSSOSessionResponse struct {
	Data struct {
		Team struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"team"`
		Key struct {
			ID     uuid.UUID `json:"id"`
			TeamID uuid.UUID `json:"team_id"`
			Scopes []string  `json:"scopes"`
			Role   string    `json:"role"`
		} `json:"key"`
		Teams []struct {
			Team struct {
				ID uuid.UUID `json:"id"`
			} `json:"team"`
			Key struct {
				ID uuid.UUID `json:"id"`
			} `json:"key"`
		} `json:"teams"`
		AuthMethod    string `json:"auth_method"`
		CanRotate     bool   `json:"can_rotate"`
		CanManageTeam bool   `json:"can_manage_team"`
	} `json:"data"`
}

func doUATGET(t *testing.T, client *nethttp.Client, target string) *nethttp.Response {
	t.Helper()

	resp, err := client.Get(target)
	require.NoError(t, err)
	return resp
}

func requireUATRedirect(t *testing.T, resp *nethttp.Response, status int) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equalf(t, status, resp.StatusCode, "body: %s", string(body))
	location := resp.Header.Get("Location")
	require.NotEmpty(t, location)
	return location
}

func decodeUATJSONResponse(t *testing.T, resp *nethttp.Response, status int, target any) {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equalf(t, status, resp.StatusCode, "body: %s", string(body))
	require.NoError(t, json.Unmarshal(body, target), string(body))
}

func findUATCookie(cookies []*nethttp.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

type uatMockOIDCProvider struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey

	mu          sync.Mutex
	nextCode    int
	nonceByCode map[string]string
}

func newUATMockOIDCProvider(t *testing.T) *uatMockOIDCProvider {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	idp := &uatMockOIDCProvider{
		privateKey:  privateKey,
		nonceByCode: make(map[string]string),
	}
	idp.server = httptest.NewServer(nethttp.HandlerFunc(idp.handle))
	t.Cleanup(idp.server.Close)
	return idp
}

func (p *uatMockOIDCProvider) URL() string {
	return p.server.URL
}

func (p *uatMockOIDCProvider) Client() *nethttp.Client {
	return p.server.Client()
}

func (p *uatMockOIDCProvider) handle(w nethttp.ResponseWriter, r *nethttp.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeUATJSON(w, nethttp.StatusOK, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"userinfo_endpoint":                     p.server.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/jwks":
		writeUATJSON(w, nethttp.StatusOK, map[string]any{
			"keys": []map[string]string{uatRSAJWK(&p.privateKey.PublicKey, uatOIDCKeyID)},
		})
	case "/authorize":
		p.handleAuthorize(w, r)
	case "/token":
		p.handleToken(w, r)
	case "/userinfo":
		writeUATJSON(w, nethttp.StatusOK, map[string]any{
			"sub":    uatSSOSubject,
			"email":  uatSSOEmail,
			"name":   uatSSOName,
			"groups": []string{uatSSOGroupID},
		})
	default:
		nethttp.NotFound(w, r)
	}
}

func (p *uatMockOIDCProvider) handleAuthorize(w nethttp.ResponseWriter, r *nethttp.Request) {
	query := r.URL.Query()
	if query.Get("client_id") != uatOIDCClientID ||
		query.Get("redirect_uri") == "" ||
		query.Get("state") == "" ||
		query.Get("nonce") == "" ||
		query.Get("code_challenge") == "" ||
		query.Get("code_challenge_method") != "S256" {
		nethttp.Error(w, "invalid authorization request", nethttp.StatusBadRequest)
		return
	}

	redirectURI, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || redirectURI.Scheme == "" || redirectURI.Host == "" {
		nethttp.Error(w, "invalid redirect_uri", nethttp.StatusBadRequest)
		return
	}

	code := p.issueCode(query.Get("nonce"))
	callbackQuery := redirectURI.Query()
	callbackQuery.Set("code", code)
	callbackQuery.Set("state", query.Get("state"))
	redirectURI.RawQuery = callbackQuery.Encode()
	nethttp.Redirect(w, r, redirectURI.String(), nethttp.StatusFound)
}

func (p *uatMockOIDCProvider) handleToken(w nethttp.ResponseWriter, r *nethttp.Request) {
	if r.Method != nethttp.MethodPost {
		nethttp.Error(w, "method not allowed", nethttp.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		nethttp.Error(w, "invalid form", nethttp.StatusBadRequest)
		return
	}
	if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code_verifier") == "" {
		nethttp.Error(w, "invalid token request", nethttp.StatusBadRequest)
		return
	}

	nonce, ok := p.consumeCode(r.Form.Get("code"))
	if !ok {
		nethttp.Error(w, "invalid code", nethttp.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	idToken, err := signUATJWT(p.privateKey, uatOIDCKeyID, map[string]any{
		"iss":    p.server.URL,
		"sub":    uatSSOSubject,
		"aud":    uatOIDCClientID,
		"exp":    now.Add(time.Hour).Unix(),
		"iat":    now.Add(-time.Minute).Unix(),
		"nonce":  nonce,
		"email":  uatSSOEmail,
		"name":   uatSSOName,
		"groups": []string{uatSSOGroupID},
	})
	if err != nil {
		nethttp.Error(w, fmt.Sprintf("failed to sign id token: %v", err), nethttp.StatusInternalServerError)
		return
	}

	writeUATJSON(w, nethttp.StatusOK, map[string]any{
		"access_token": "uat-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func (p *uatMockOIDCProvider) issueCode(nonce string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.nextCode++
	code := fmt.Sprintf("uat-code-%d", p.nextCode)
	p.nonceByCode[code] = nonce
	return code
}

func (p *uatMockOIDCProvider) consumeCode(code string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	nonce, ok := p.nonceByCode[code]
	if ok {
		delete(p.nonceByCode, code)
	}
	return nonce, ok
}

func writeUATJSON(w nethttp.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func signUATJWT(key *rsa.PrivateKey, keyID string, claims map[string]any) (string, error) {
	header := map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
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

func uatRSAJWK(key *rsa.PublicKey, keyID string) map[string]string {
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
