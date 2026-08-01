package http

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
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestControlIdentityHTTPHandlersAndAdminGroupLifecycle(t *testing.T) {
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
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{controlHTTPRSAJWK(&privateKey.PublicKey, "control-http-key")}}))
		case "/token":
			idToken := controlHTTPSignedOIDCToken(t, privateKey, "control-http-key", map[string]any{
				"iss":    oidcServer.URL,
				"sub":    "entra-http-admin",
				"tid":    "tenant-id",
				"aud":    "control-http-client",
				"exp":    now.Add(time.Hour).Unix(),
				"iat":    now.Add(-time.Minute).Unix(),
				"nonce":  nonce,
				"email":  "admin@example.test",
				"name":   "HTTP Admin",
				"groups": []string{"entra-control-admins"},
			})
			w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"access_token": "control-http-access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"id_token":     idToken,
			}))
		case "/userinfo":
			w.Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"sub": "entra-http-admin"}))
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
		ClientID:  "control-http-client",
		Enabled:   true,
	}
	repo := newControlIdentityHTTPRepository()
	repo.groups = []*domain.ControlAdminGroup{{
		ID:         uuid.New(),
		ProviderID: providerID,
		GroupID:    "entra-control-admins",
		GroupName:  "Control administrators",
		Enabled:    true,
	}}
	ssoRepo := &controlIdentityHTTPSSORepository{providers: map[uuid.UUID]*domain.SSOProvider{providerID: provider}}
	identity := service.NewControlIdentityService(repo, ssoRepo, service.ControlIdentityConfig{
		HTTPClient: oidcServer.Client(),
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{
			ControlPublicBaseURL: "https://control.example.test",
			StateTTL:             time.Minute,
			SessionTTL:           time.Hour,
		}},
		Now: func() time.Time { return now },
	})
	handler := &controlPortalHandler{controlIdentity: identity}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	registerControlIdentityRoutes(e, handler)

	request := httptest.NewRequest(nethttp.MethodGet, "/control/auth/providers", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), providerID.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/control/auth/start/"+providerID.String(), nil)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusFound, response.Code, response.Body.String())
	startURL, err := url.Parse(response.Header().Get(echo.HeaderLocation))
	require.NoError(t, err)
	stateToken := startURL.Query().Get("state")
	require.NotEmpty(t, stateToken)
	nonce = repo.states[service.HashSSOToken(stateToken)].Nonce

	request = httptest.NewRequest(nethttp.MethodGet, "/control/auth/callback?state="+url.QueryEscape(stateToken)+"&code=control-http-code", nil)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusFound, response.Code, response.Body.String())
	require.Equal(t, "/", response.Header().Get(echo.HeaderLocation))
	cookies := response.Result().Cookies()
	require.Len(t, cookies, 2)
	var sessionToken string
	for _, cookie := range cookies {
		if cookie.Name == service.ControlSessionCookieName {
			sessionToken = cookie.Value
			require.True(t, cookie.Secure)
			require.Equal(t, "/", cookie.Path)
		}
		if cookie.Name == service.ControlCSRFCookieName {
			require.True(t, cookie.Secure)
			require.Equal(t, "/", cookie.Path)
		}
	}
	require.NotEmpty(t, sessionToken)

	c, rec := controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": providerID.String()})
	handler.controlIdentity = identity
	require.NoError(t, handler.listControlAdminGroups(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)

	c, rec = controlDirectoryContext(nethttp.MethodPost, `{"group_id":"entra-observers","group_name":"Observers","enabled":true}`, map[string]string{"providerId": providerID.String()})
	require.NoError(t, handler.createControlAdminGroup(c))
	require.Equal(t, nethttp.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Data controlAdminGroupResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEqual(t, uuid.Nil, created.Data.ID)

	c, rec = controlDirectoryContext(nethttp.MethodPatch, `{"group_id":"entra-observers","group_name":"Read-only observers","enabled":false}`, map[string]string{"providerId": providerID.String(), "groupId": created.Data.ID.String()})
	require.NoError(t, handler.updateControlAdminGroup(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"enabled":false`)

	c, rec = controlDirectoryContext(nethttp.MethodDelete, "", map[string]string{"providerId": providerID.String(), "groupId": created.Data.ID.String()})
	require.NoError(t, handler.deleteControlAdminGroup(c))
	require.Equal(t, nethttp.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"status":"retired"`)

	request = httptest.NewRequest(nethttp.MethodPost, "/control/auth/logout", nil)
	request.AddCookie(&nethttp.Cookie{Name: service.ControlSessionCookieName, Value: sessionToken})
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code, response.Body.String())
	require.Equal(t, service.HashSSOToken(sessionToken), repo.deletedSession)
	requireControlIdentityCookiesCleared(t, response.Result().Cookies())

	request = httptest.NewRequest(nethttp.MethodGet, "/control/auth/callback?error=access_denied", nil)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusUnauthorized, response.Code)
}

func TestControlIdentityHTTPHelpersMapSafeResponses(t *testing.T) {
	t.Parallel()

	group := controlAdminGroupRequest{GroupID: "admins", GroupName: "Admins"}.toDomain(uuid.New(), uuid.New())
	require.True(t, group.Enabled)
	disabled := false
	group = (controlAdminGroupRequest{GroupID: "admins", GroupName: "Admins", Enabled: &disabled}).toDomain(uuid.New(), uuid.New())
	require.False(t, group.Enabled)

	retiredAt := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	response := toControlAdminGroup(&domain.ControlAdminGroup{ID: uuid.New(), ProviderID: uuid.New(), GroupID: "admins", GroupName: "Admins", Enabled: false, RetiredAt: &retiredAt, CreatedAt: retiredAt, UpdatedAt: retiredAt})
	require.NotNil(t, response.RetiredAt)
	require.Equal(t, controlAdminGroupResponse{}, toControlAdminGroup(nil))
	_, err := controlIdentityCallbackURL(echo.New().NewContext(httptest.NewRequest(nethttp.MethodGet, "/", nil), httptest.NewRecorder()), nil)
	require.ErrorIs(t, err, service.ErrControlSSOUnavailable)

	for _, testCase := range []struct {
		err  error
		code httperr.ErrorCode
	}{
		{service.ErrControlAccessDenied, httperr.AUTH_INVALID},
		{service.ErrControlSessionInvalid, httperr.AUTH_INVALID},
		{service.ErrControlSSOUnavailable, httperr.SERVICE_UNAVAILABLE},
		{assertError("control admin group not found"), httperr.NOT_FOUND},
		{assertError("sso provider ID is required"), httperr.VALIDATION_ERROR},
		{assertError("storage unavailable"), httperr.INTERNAL_ERROR},
	} {
		mapped := controlIdentityHTTPError(testCase.err)
		httpError, ok := mapped.(*httperr.APIError)
		require.True(t, ok)
		require.Equal(t, testCase.code, httpError.GetCode())
	}
}

func TestControlIdentityHTTPHandlersValidateInputAndFailSafely(t *testing.T) {
	t.Parallel()

	providerID := uuid.New()
	providerIDText := providerID.String()
	runtimeFailure := service.NewControlIdentityService(newControlIdentityHTTPRepository(), &controlIdentityHTTPSSORepository{providers: map[uuid.UUID]*domain.SSOProvider{}}, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{err: errors.New("runtime unavailable")},
	})
	handler := &controlPortalHandler{controlIdentity: runtimeFailure}
	c, _ := controlDirectoryContext(nethttp.MethodGet, "", nil)
	require.Error(t, handler.controlIdentityProviders(c))

	registerControlIdentityRoutes(nil, handler)
	registerControlIdentityRoutes(echo.New(), nil)
	registerControlIdentityRoutes(echo.New(), &controlPortalHandler{})

	readyUnavailable := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test"}},
	})
	handler.controlIdentity = readyUnavailable
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": "not-a-uuid"})
	require.Error(t, handler.startControlIdentityLogin(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": providerIDText})
	require.Error(t, handler.startControlIdentityLogin(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", nil)
	require.Error(t, handler.completeControlIdentityLogin(c))

	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": "not-a-uuid"})
	require.Error(t, handler.listControlAdminGroups(c))
	c, _ = controlDirectoryContext(nethttp.MethodGet, "", map[string]string{"providerId": providerIDText})
	require.Error(t, handler.listControlAdminGroups(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "", map[string]string{"providerId": "not-a-uuid"})
	require.Error(t, handler.createControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, "{", map[string]string{"providerId": providerIDText})
	require.Error(t, handler.createControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodPost, `{"group_id":"admins"}`, map[string]string{"providerId": providerIDText})
	require.Error(t, handler.createControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{}", map[string]string{"providerId": "not-a-uuid", "groupId": uuid.NewString()})
	require.Error(t, handler.updateControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{}", map[string]string{"providerId": providerIDText, "groupId": "not-a-uuid"})
	require.Error(t, handler.updateControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, "{", map[string]string{"providerId": providerIDText, "groupId": uuid.NewString()})
	require.Error(t, handler.updateControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodPatch, `{"group_id":"admins"}`, map[string]string{"providerId": providerIDText, "groupId": uuid.NewString()})
	require.Error(t, handler.updateControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodDelete, "", map[string]string{"providerId": "not-a-uuid", "groupId": uuid.NewString()})
	require.Error(t, handler.deleteControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodDelete, "", map[string]string{"providerId": providerIDText, "groupId": "not-a-uuid"})
	require.Error(t, handler.deleteControlAdminGroup(c))
	c, _ = controlDirectoryContext(nethttp.MethodDelete, "", map[string]string{"providerId": providerIDText, "groupId": uuid.NewString()})
	require.Error(t, handler.deleteControlAdminGroup(c))

	emptyOrigin := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{RuntimeConfig: controlIdentityHTTPRuntime{}})
	_, err := controlIdentityCallbackURL(echo.New().NewContext(httptest.NewRequest(nethttp.MethodGet, "/", nil), httptest.NewRecorder()), emptyOrigin)
	require.ErrorIs(t, err, service.ErrControlSSOUnavailable)
	_, err = controlIdentityCallbackURL(echo.New().NewContext(httptest.NewRequest(nethttp.MethodGet, "/", nil), httptest.NewRecorder()), runtimeFailure)
	require.ErrorContains(t, err, "runtime unavailable")

	logoutFailure := service.NewControlIdentityService(controlIdentityLogoutFailureRepository{}, nil, service.ControlIdentityConfig{})
	handler.controlIdentity = logoutFailure
	c, logoutFailureRec := controlDirectoryContext(nethttp.MethodPost, "", nil)
	c.Request().AddCookie(&nethttp.Cookie{Name: service.ControlSessionCookieName, Value: "session"})
	require.Error(t, handler.logoutControlIdentity(c))
	requireControlIdentityCookiesCleared(t, logoutFailureRec.Result().Cookies())
	handler.controlIdentity = readyUnavailable
	c, rec := controlDirectoryContext(nethttp.MethodPost, "", nil)
	require.NoError(t, handler.logoutControlIdentity(c))
	require.Equal(t, nethttp.StatusNoContent, rec.Code)
}

func requireControlIdentityCookiesCleared(t *testing.T, cookies []*nethttp.Cookie) {
	t.Helper()
	require.Len(t, cookies, 4)
	cleared := make(map[string]bool, len(cookies))
	for _, cookie := range cookies {
		require.Empty(t, cookie.Value)
		require.Equal(t, -1, cookie.MaxAge)
		cleared[cookie.Name+"|"+cookie.Path] = true
	}
	require.Equal(t, map[string]bool{
		service.ControlSessionCookieName + "|/":        true,
		service.ControlSessionCookieName + "|/control": true,
		service.ControlCSRFCookieName + "|/":           true,
		service.ControlCSRFCookieName + "|/control":    true,
	}, cleared)
}

type controlIdentityHTTPRuntime struct {
	config service.SSORuntimeConfig
	err    error
}

type controlIdentityLogoutFailureRepository struct {
	repository.ControlIdentityRepository
}

func (controlIdentityLogoutFailureRepository) DeleteControlSession(context.Context, string) error {
	return errors.New("delete control session failed")
}

func (r controlIdentityHTTPRuntime) SSORuntimeConfig(context.Context) (service.SSORuntimeConfig, error) {
	if r.err != nil {
		return service.SSORuntimeConfig{}, r.err
	}
	return r.config, nil
}

type controlIdentityHTTPRepository struct {
	repository.ControlIdentityRepository
	groups         []*domain.ControlAdminGroup
	states         map[string]*domain.ControlOAuthState
	sessions       map[string]*domain.ControlSession
	deletedSession string
}

func newControlIdentityHTTPRepository() *controlIdentityHTTPRepository {
	return &controlIdentityHTTPRepository{
		states:   make(map[string]*domain.ControlOAuthState),
		sessions: make(map[string]*domain.ControlSession),
	}
}

func (r *controlIdentityHTTPRepository) ListControlAdminGroups(_ context.Context, providerID uuid.UUID) ([]*domain.ControlAdminGroup, error) {
	items := make([]*domain.ControlAdminGroup, 0)
	for _, group := range r.groups {
		if group != nil && group.ProviderID == providerID {
			copy := *group
			items = append(items, &copy)
		}
	}
	return items, nil
}

func (r *controlIdentityHTTPRepository) CreateControlAdminGroup(_ context.Context, group *domain.ControlAdminGroup) error {
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	copy := *group
	r.groups = append(r.groups, &copy)
	return nil
}

func (r *controlIdentityHTTPRepository) UpdateControlAdminGroup(_ context.Context, group *domain.ControlAdminGroup) error {
	for index, existing := range r.groups {
		if existing != nil && existing.ID == group.ID && existing.ProviderID == group.ProviderID {
			copy := *group
			r.groups[index] = &copy
		}
	}
	return nil
}

func (r *controlIdentityHTTPRepository) RetireControlAdminGroup(_ context.Context, providerID, groupID uuid.UUID) error {
	for _, group := range r.groups {
		if group != nil && group.ProviderID == providerID && group.ID == groupID {
			group.Enabled = false
			now := time.Now().UTC()
			group.RetiredAt = &now
		}
	}
	return nil
}

func (r *controlIdentityHTTPRepository) CreateControlOAuthState(_ context.Context, state domain.ControlOAuthState) error {
	copy := state
	r.states[state.StateHash] = &copy
	return nil
}

func (r *controlIdentityHTTPRepository) ConsumeControlOAuthState(_ context.Context, stateHash string) (*domain.ControlOAuthState, error) {
	state := r.states[stateHash]
	if state == nil {
		return nil, nil
	}
	delete(r.states, stateHash)
	copy := *state
	return &copy, nil
}

func (r *controlIdentityHTTPRepository) DeleteExpiredControlOAuthStates(_ context.Context, now time.Time) error {
	for stateHash, state := range r.states {
		if !state.ExpiresAt.After(now) {
			delete(r.states, stateHash)
		}
	}
	return nil
}

func (r *controlIdentityHTTPRepository) CreateControlSession(_ context.Context, session domain.ControlSession) error {
	copy := session
	copy.GroupIDs = append([]string(nil), session.GroupIDs...)
	r.sessions[session.SessionHash] = &copy
	return nil
}

func (r *controlIdentityHTTPRepository) GetControlSession(_ context.Context, sessionHash string) (*domain.ControlSession, error) {
	session := r.sessions[sessionHash]
	if session == nil {
		return nil, nil
	}
	copy := *session
	copy.GroupIDs = append([]string(nil), session.GroupIDs...)
	return &copy, nil
}

func (r *controlIdentityHTTPRepository) DeleteControlSession(_ context.Context, sessionHash string) error {
	r.deletedSession = sessionHash
	delete(r.sessions, sessionHash)
	return nil
}

func (r *controlIdentityHTTPRepository) DeleteExpiredControlSessions(_ context.Context, now time.Time) error {
	for sessionHash, session := range r.sessions {
		if !session.ExpiresAt.After(now) {
			delete(r.sessions, sessionHash)
		}
	}
	return nil
}

type controlIdentityHTTPSSORepository struct {
	repository.SSORepository
	providers  map[uuid.UUID]*domain.SSOProvider
	identities map[uuid.UUID]*domain.SSOIdentity
}

func (r *controlIdentityHTTPSSORepository) ListEnabledProviders(context.Context) ([]*domain.SSOProvider, error) {
	items := make([]*domain.SSOProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		copy := *provider
		items = append(items, &copy)
	}
	return items, nil
}

func (r *controlIdentityHTTPSSORepository) GetProvider(_ context.Context, providerID uuid.UUID) (*domain.SSOProvider, error) {
	provider := r.providers[providerID]
	if provider == nil {
		return nil, nil
	}
	copy := *provider
	return &copy, nil
}

func (r *controlIdentityHTTPSSORepository) UpsertIdentity(_ context.Context, identity *domain.SSOIdentity) error {
	if identity.ID == uuid.Nil {
		identity.ID = uuid.New()
	}
	if r.identities == nil {
		r.identities = make(map[uuid.UUID]*domain.SSOIdentity)
	}
	copy := *identity
	r.identities[identity.ID] = &copy
	return nil
}

func (r *controlIdentityHTTPSSORepository) GetIdentity(_ context.Context, identityID uuid.UUID) (*domain.SSOIdentity, error) {
	identity := r.identities[identityID]
	if identity == nil {
		return nil, nil
	}
	copy := *identity
	return &copy, nil
}

func controlHTTPSignedOIDCToken(t *testing.T, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"})
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func controlHTTPRSAJWK(key *rsa.PublicKey, keyID string) map[string]string {
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
