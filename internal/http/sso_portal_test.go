package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
)

func TestControlPortalSSOProviderAndMappingFlows(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	mappingID := uuid.New()
	teamID := uuid.New()
	repo := &httpSSORepoStub{
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: httpSSOProvider(providerID, "Enterprise IdP", now),
		},
		mappings: []*domain.SSOGroupMapping{{
			ID:         mappingID,
			ProviderID: providerID,
			TeamID:     teamID,
			TeamName:   "Research",
			GroupID:    "group-read",
			GroupName:  "Read group",
			Scopes:     []string{service.APIKeyScopeRead},
			Role:       service.APIKeyRoleMember,
			Enabled:    true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}},
		now: now,
	}
	server := controlPortalSSOServer(t, repo, now)

	rec := serveControlSSO(server, nethttp.MethodGet, "/control/api/sso/providers", "")
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"name":"Enterprise IdP"`)

	createBody := `{"name":"Azure","kind":"azure_ad","issuer_url":"https://login.microsoftonline.com/tenant/v2.0","client_id":"azure-client","client_secret_env":"AZURE_SECRET","scopes":["profile","openid"],"group_claims":["groups"],"enabled":true}`
	rec = serveControlSSO(server, nethttp.MethodPost, "/control/api/sso/providers", createBody)
	require.Equal(t, nethttp.StatusCreated, rec.Code)
	require.Contains(t, rec.Body.String(), `"kind":"azure_ad"`)

	updateBody := `{"name":"PingOne","kind":"pingone","issuer_url":"https://auth.pingone.com/example/as","client_id":"ping-client","scopes":["openid","email"],"group_claims":["memberOf"],"enabled":false}`
	rec = serveControlSSO(server, nethttp.MethodPatch, "/control/api/sso/providers/"+providerID.String(), updateBody)
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":false`)
	require.Equal(t, domain.SSOProviderKindPingOne, repo.providers[providerID].Kind)

	rec = serveControlSSO(server, nethttp.MethodGet, "/control/api/sso/providers/"+providerID.String()+"/mappings", "")
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"group_id":"group-read"`)

	createMapping := `{"team_id":"` + teamID.String() + `","group_id":"group-write","group_name":"Writers","scopes":["read","write"],"role":"manager","enabled":true}`
	rec = serveControlSSO(server, nethttp.MethodPost, "/control/api/sso/providers/"+providerID.String()+"/mappings", createMapping)
	require.Equal(t, nethttp.StatusCreated, rec.Code)
	require.Contains(t, rec.Body.String(), `"role":"manager"`)

	updateMapping := `{"team_id":"` + teamID.String() + `","group_id":"group-admin","group_name":"Admins","scopes":["read"],"role":"member","enabled":false}`
	rec = serveControlSSO(server, nethttp.MethodPatch, "/control/api/sso/providers/"+providerID.String()+"/mappings/"+mappingID.String(), updateMapping)
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":false`)

	rec = serveControlSSO(server, nethttp.MethodDelete, "/control/api/sso/providers/"+providerID.String()+"/mappings/"+mappingID.String(), "")
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Equal(t, mappingID, repo.deletedMappingID)

	rec = serveControlSSO(server, nethttp.MethodDelete, "/control/api/sso/providers/"+providerID.String(), "")
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Equal(t, providerID, repo.deletedProviderID)

	rec = serveControlSSO(server, nethttp.MethodPost, "/control/api/sso/providers", `{"kind":"bad"}`)
	require.Equal(t, nethttp.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "sso provider name is required")
}

func TestUserPortalSSOHandlers(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	identityID := uuid.New()
	teamOneID := uuid.New()
	teamTwoID := uuid.New()
	profileOneID := uuid.New()
	profileTwoID := uuid.New()
	sessionToken := "session-token"
	csrfToken := "csrf-token"
	repo := &httpSSORepoStub{
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: httpSSOProvider(providerID, "Enterprise IdP", now),
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject-123",
			Groups:     []string{"group-one", "group-two"},
			Status:     "active",
			CheckedAt:  now,
			ExpiresAt:  now.Add(time.Hour),
		},
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {
				ID:         identityID,
				ProviderID: providerID,
				Subject:    "subject-123",
				Email:      "user@example.com",
			},
		},
		mappings: []*domain.SSOGroupMapping{
			httpSSOMapping(providerID, teamOneID, "Team One", "group-one", service.APIKeyRoleMember, now),
			httpSSOMapping(providerID, teamTwoID, "Team Two", "group-two", service.APIKeyRoleManager, now),
		},
		sessions: map[string]*domain.SSOSession{
			service.HashSSOToken(sessionToken): {
				SessionHash:   service.HashSSOToken(sessionToken),
				IdentityID:    identityID,
				ProviderID:    providerID,
				TeamProfileID: profileOneID,
				TeamID:        teamOneID,
				CSRFHash:      service.HashSSOToken(csrfToken),
				ExpiresAt:     now.Add(time.Hour),
				CreatedAt:     now,
			},
		},
		now: now,
	}
	repo.teamProfiles = []*domain.SSOTeamProfile{
		httpSSOTeamProfile(identityID, providerID, profileOneID, teamOneID, "Team One", service.APIKeyRoleMember, now),
		httpSSOTeamProfile(identityID, providerID, profileTwoID, teamTwoID, "Team Two", service.APIKeyRoleManager, now),
	}
	repo.ssoProfiles = map[uuid.UUID]*domain.APIKey{
		profileOneID: &repo.teamProfiles[0].Profile,
		profileTwoID: &repo.teamProfiles[1].Profile,
	}
	handler := &userPortalHandler{
		sso: service.NewSSOService(repo, service.SSOConfig{
			PublicBaseURL: "https://portal.example.com",
			Now:           func() time.Time { return now },
		}),
	}

	c, rec := userSSOContext(nethttp.MethodGet, "/ui/api/sso/providers", "", "")
	require.NoError(t, handler.ssoProviders(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"name":"Enterprise IdP"`)

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/session", "", sessionToken)
	session, err := handler.currentSSOSession(c)
	require.NoError(t, err)
	require.Equal(t, "sso", session.AuthMethod)
	require.False(t, session.CanRotate)
	require.Len(t, session.Teams, 2)
	require.False(t, session.Teams[0].CanRotate)
	require.False(t, session.Teams[1].CanRotate)
	require.Equal(t, "Team One", session.Team.Name)

	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/team", `{"profile_id":"`+profileTwoID.String()+`"}`, sessionToken)
	setUserSSOPrincipal(c, profileOneID, teamOneID)
	require.NoError(t, handler.switchSSOTeam(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Equal(t, service.HashSSOToken(sessionToken), repo.updatedSessionHash)
	require.Contains(t, rec.Body.String(), `"name":"Team Two"`)

	c, rec = userSSOContext(nethttp.MethodPost, "/ui/api/sso/logout", "", sessionToken)
	addSSOCSRF(c, csrfToken)
	require.NoError(t, handler.logoutSSO(c))
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Equal(t, service.HashSSOToken(sessionToken), repo.deletedSessionHash)
	require.Contains(t, rec.Header().Values("Set-Cookie")[0], service.SSOSessionCookieName+"=;")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/callback", "", "")
	req := c.Request()
	req.Host = "internal.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "public.example")
	callbackURL, err := handler.ssoCallbackURL(c.Request().Context())
	require.NoError(t, err)
	require.Equal(t, "https://portal.example.com/ui/api/sso/callback", callbackURL)
	handler.sso = service.NewSSOService(repo, service.SSOConfig{Now: func() time.Time { return now }})
	_, err = handler.ssoCallbackURL(c.Request().Context())
	require.ErrorContains(t, err, "sso public base url is not configured")
}

func TestUserPortalSSOErrorBranches(t *testing.T) {
	handler := &userPortalHandler{}

	c, _ := userSSOContext(nethttp.MethodGet, "/ui/api/sso/start/not-a-uuid", "", "")
	c.SetParamNames("providerId")
	c.SetParamValues("not-a-uuid")
	require.ErrorContains(t, handler.startSSO(c), "sso is not configured")

	handler.sso = service.NewSSOService(&httpSSORepoStub{}, service.SSOConfig{})
	require.ErrorContains(t, handler.startSSO(c), "invalid sso provider ID format")

	require.ErrorContains(t, (&userPortalHandler{}).switchSSOTeam(c), "sso is not configured")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/team", `{}`, "")
	require.ErrorContains(t, handler.switchSSOTeam(c), "authentication required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/team", `{}`, "session-token")
	setUserAPIKeyPrincipal(c, uuid.New(), uuid.New())
	require.ErrorContains(t, handler.switchSSOTeam(c), "sso session required")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/team", `{`, "session-token")
	setUserSSOPrincipal(c, uuid.New(), uuid.New())
	require.ErrorContains(t, handler.switchSSOTeam(c), "malformed JSON body")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/callback?error=access_denied", "", "")
	require.ErrorContains(t, handler.completeSSO(c), "sso login failed")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/team", `{"profile_id":"bad"}`, "session-token")
	setUserSSOPrincipal(c, uuid.New(), uuid.New())
	require.ErrorContains(t, handler.switchSSOTeam(c), "invalid SSO profile ID format")

	c, _ = userSSOContext(nethttp.MethodPost, "/ui/api/sso/logout", "", "session-token")
	require.ErrorContains(t, handler.logoutSSO(c), "invalid sso csrf token")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/session", "", "")
	_, err := ssoSessionTokenFromRequest(c)
	require.ErrorContains(t, err, "authentication required")

	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/session", "", "missing-session")
	_, err = handler.currentSSOSession(c)
	require.ErrorContains(t, err, "invalid sso session")

	handler.sso = service.NewSSOService(&httpSSORepoStub{listProvidersErr: errors.New("list providers failed")}, service.SSOConfig{PublicBaseURL: "https://portal.example.com"})
	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/providers", "", "")
	require.ErrorContains(t, handler.ssoProviders(c), "list providers failed")

	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	discovery := httpSSODiscoveryServer(t)
	defer discovery.Close()
	provider := httpSSOProvider(providerID, "Enterprise IdP", now)
	provider.IssuerURL = discovery.URL
	handler.sso = service.NewSSOService(&httpSSORepoStub{
		providers: map[uuid.UUID]*domain.SSOProvider{providerID: provider},
		now:       now,
	}, service.SSOConfig{HTTPClient: discovery.Client(), Now: func() time.Time { return now }})
	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/start/"+providerID.String(), "", "")
	c.SetParamNames("providerId")
	c.SetParamValues(providerID.String())
	require.ErrorContains(t, handler.startSSO(c), "sso public base url is not configured")

	handler.sso = service.NewSSOService(&httpSSORepoStub{
		providers: map[uuid.UUID]*domain.SSOProvider{providerID: provider},
		now:       now,
	}, service.SSOConfig{PublicBaseURL: "https://portal.example.com", HTTPClient: discovery.Client(), Now: func() time.Time { return now }})
	c, rec := userSSOContext(nethttp.MethodGet, "/ui/api/sso/start/"+providerID.String(), "", "")
	c.SetParamNames("providerId")
	c.SetParamValues(providerID.String())
	require.NoError(t, handler.startSSO(c))
	require.Equal(t, nethttp.StatusFound, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), discovery.URL+"/authorize")

	require.ErrorContains(t, userPortalSSOError(service.ErrSSOSessionInvalid), "invalid sso session")
	require.ErrorContains(t, userPortalSSOError(service.ErrSSOCSRFInvalid), "invalid sso csrf token")
	require.ErrorContains(t, userPortalSSOError(service.ErrSSOAccessDenied), "sso access denied")
	require.ErrorContains(t, userPortalSSOError(service.NewSSOSetupError(service.SSOSetupMappingMissing, service.ErrSSOAccessDenied)), "no mapping matched")
	require.ErrorContains(t, userPortalSSOError(errors.New("issuer returned tenant detail")), "sso authentication failed")
	require.NotContains(t, userPortalSSOError(errors.New("issuer returned tenant detail")).Error(), "tenant detail")
}

func TestControlPortalSSOErrorBranches(t *testing.T) {
	e := echo.New()
	handler := &controlPortalHandler{}

	for name, call := range map[string]func(echo.Context) error{
		"list providers":  handler.listSSOProviders,
		"create provider": handler.createSSOProvider,
		"update provider": func(c echo.Context) error {
			c.SetParamNames("providerId")
			c.SetParamValues(uuid.NewString())
			return handler.updateSSOProvider(c)
		},
		"delete provider": func(c echo.Context) error {
			c.SetParamNames("providerId")
			c.SetParamValues(uuid.NewString())
			return handler.deleteSSOProvider(c)
		},
		"list mappings": func(c echo.Context) error {
			c.SetParamNames("providerId")
			c.SetParamValues(uuid.NewString())
			return handler.listSSOMappings(c)
		},
		"create mapping": func(c echo.Context) error {
			c.SetParamNames("providerId")
			c.SetParamValues(uuid.NewString())
			return handler.createSSOMapping(c)
		},
		"update mapping": func(c echo.Context) error {
			c.SetParamNames("providerId", "mappingId")
			c.SetParamValues(uuid.NewString(), uuid.NewString())
			return handler.updateSSOMapping(c)
		},
		"delete mapping": func(c echo.Context) error {
			c.SetParamNames("providerId", "mappingId")
			c.SetParamValues(uuid.NewString(), uuid.NewString())
			return handler.deleteSSOMapping(c)
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(nethttp.MethodGet, "/", strings.NewReader(`{`))
			req.Header.Set("Content-Type", "application/json")
			err := call(e.NewContext(req, httptest.NewRecorder()))
			require.ErrorContains(t, err, "sso service unavailable")
		})
	}

	handler.sso = service.NewSSOService(&httpSSORepoStub{}, service.SSOConfig{})
	req := httptest.NewRequest(nethttp.MethodPatch, "/", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId")
	c.SetParamValues(uuid.NewString())
	require.ErrorContains(t, handler.updateSSOProvider(c), "malformed JSON body")

	req = httptest.NewRequest(nethttp.MethodPost, "/", strings.NewReader(`{`))
	req.Header.Set("Content-Type", "application/json")
	c = e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId")
	c.SetParamValues(uuid.NewString())
	require.ErrorContains(t, handler.createSSOMapping(c), "malformed JSON body")

	req = httptest.NewRequest(nethttp.MethodDelete, "/", nil)
	c = e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId", "mappingId")
	c.SetParamValues("bad", uuid.NewString())
	require.ErrorContains(t, handler.deleteSSOMapping(c), "invalid SSO provider ID format")

	req = httptest.NewRequest(nethttp.MethodPatch, "/", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	c = e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId", "mappingId")
	c.SetParamValues(uuid.NewString(), "bad")
	require.ErrorContains(t, handler.updateSSOMapping(c), "invalid SSO group mapping ID format")

	backendErr := errors.New("sso backend failed")
	handler.sso = service.NewSSOService(&httpSSORepoStub{listProvidersErr: backendErr}, service.SSOConfig{})
	req = httptest.NewRequest(nethttp.MethodGet, "/", nil)
	require.ErrorIs(t, handler.listSSOProviders(e.NewContext(req, httptest.NewRecorder())), backendErr)

	handler.sso = service.NewSSOService(&httpSSORepoStub{listMappingsErr: backendErr}, service.SSOConfig{})
	req = httptest.NewRequest(nethttp.MethodGet, "/", nil)
	c = e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId")
	c.SetParamValues(uuid.NewString())
	require.ErrorIs(t, handler.listSSOMappings(c), backendErr)

	handler.sso = service.NewSSOService(&httpSSORepoStub{deleteProviderErr: backendErr}, service.SSOConfig{})
	req = httptest.NewRequest(nethttp.MethodDelete, "/", nil)
	c = e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId")
	c.SetParamValues(uuid.NewString())
	require.ErrorIs(t, handler.deleteSSOProvider(c), backendErr)

	handler.sso = service.NewSSOService(&httpSSORepoStub{deleteMappingErr: backendErr}, service.SSOConfig{})
	req = httptest.NewRequest(nethttp.MethodDelete, "/", nil)
	c = e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("providerId", "mappingId")
	c.SetParamValues(uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, handler.deleteSSOMapping(c), backendErr)

	require.Equal(t, controlSSOProviderResponse{}, toControlSSOProvider(nil))
	require.Equal(t, controlSSOGroupMappingResponse{}, toControlSSOGroupMapping(nil))
	require.NoError(t, controlSSOServiceError(nil))
	databaseErr := errors.New("database down")
	require.ErrorIs(t, controlSSOServiceError(databaseErr), databaseErr)
}

func TestUserPortalSSORoutesAndCookies(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	providerID := uuid.New()
	teamID := uuid.New()
	profileID := uuid.New()
	identityID := uuid.New()
	sessionToken := "session-token"
	repo := &httpSSORepoStub{
		providers: map[uuid.UUID]*domain.SSOProvider{
			providerID: httpSSOProvider(providerID, "Enterprise IdP", now),
		},
		cache: &domain.SSOEntitlementCache{
			ProviderID: providerID,
			Subject:    "subject-123",
			Groups:     []string{"group-one"},
			Status:     "active",
			CheckedAt:  now,
			ExpiresAt:  now.Add(time.Hour),
		},
		mappings: []*domain.SSOGroupMapping{
			httpSSOMapping(providerID, teamID, "Team One", "group-one", service.APIKeyRoleMember, now),
		},
		identities: map[uuid.UUID]*domain.SSOIdentity{
			identityID: {ID: identityID, ProviderID: providerID, Subject: "subject-123"},
		},
		sessions: map[string]*domain.SSOSession{
			service.HashSSOToken(sessionToken): {
				SessionHash:   service.HashSSOToken(sessionToken),
				IdentityID:    identityID,
				ProviderID:    providerID,
				TeamProfileID: profileID,
				TeamID:        teamID,
				ExpiresAt:     now.Add(time.Hour),
			},
		},
		now: now,
	}
	repo.teamProfiles = []*domain.SSOTeamProfile{
		httpSSOTeamProfile(identityID, providerID, profileID, teamID, "Team One", service.APIKeyRoleMember, now),
	}
	repo.ssoProfiles = map[uuid.UUID]*domain.APIKey{profileID: &repo.teamProfiles[0].Profile}
	ssoSvc := service.NewSSOService(repo, service.SSOConfig{
		PublicBaseURL: "https://portal.example.com",
		Now:           func() time.Time { return now },
	})

	e := NewServer(config.Config{
		HTTPMaxBodyBytes:   1048576,
		RateLimitPerMinute: 100,
	}, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:   &userPortalAuthRepo{},
		RateLimitSvc: service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		SSOService:   ssoSvc,
		Config:       &config.Config{RateLimitPerMinute: 100},
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/ui/api/sso/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"Enterprise IdP"`)

	req = httptest.NewRequest(nethttp.MethodGet, "/ui/api/session", nil)
	req.AddCookie(&nethttp.Cookie{Name: service.SSOSessionCookieName, Value: sessionToken})
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"auth_method":"sso"`)
	require.Contains(t, rec.Body.String(), `"name":"Team One"`)

	req = httptest.NewRequest(nethttp.MethodPost, "/ui/api/sso/logout", nil)
	csrfToken := "csrf-token"
	req.AddCookie(&nethttp.Cookie{Name: service.SSOSessionCookieName, Value: sessionToken})
	req.AddCookie(&nethttp.Cookie{Name: service.SSOCSRFCookieName, Value: csrfToken})
	req.Header.Set(service.SSOCSRFHeaderName, csrfToken)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, nethttp.StatusOK, rec.Code)
	require.Equal(t, service.HashSSOToken(sessionToken), repo.deletedSessionHash)

	c, rec := userSSOContext(nethttp.MethodGet, "/ui/api/sso/callback", "", "")
	setSSOCookie(c, service.SSOSessionCookieName, "value", true, now.Add(time.Hour), true)
	require.Contains(t, rec.Header().Get("Set-Cookie"), service.SSOSessionCookieName+"=value")

	handler := &userPortalHandler{}
	c, _ = userSSOContext(nethttp.MethodGet, "/ui/api/sso/providers", "", "")
	require.NoError(t, handler.ssoProviders(c))
	_, err := handler.currentSSOSession(c)
	require.ErrorContains(t, err, "authentication required")
	require.ErrorContains(t, handler.completeSSO(c), "sso is not configured")
}

func TestUserPortalPublicSSOStartIsRateLimited(t *testing.T) {
	cfg := &config.Config{
		HTTPMaxBodyBytes:   1048576,
		RateLimitPerMinute: 1,
	}
	e := NewServer(*cfg, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:   &userPortalAuthRepo{},
		RateLimitSvc: service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		SSOService:   service.NewSSOService(&httpSSORepoStub{}, service.SSOConfig{PublicBaseURL: "https://portal.example.com"}),
		Config:       cfg,
	})

	target := "/ui/api/sso/start/" + uuid.NewString()
	req := httptest.NewRequest(nethttp.MethodGet, target, nil)
	req.RemoteAddr = "198.51.100.10:1234"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.NotEqual(t, nethttp.StatusTooManyRequests, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-RateLimit-Limit"))

	req = httptest.NewRequest(nethttp.MethodGet, target, nil)
	req.RemoteAddr = "198.51.100.10:1234"
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, nethttp.StatusTooManyRequests, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-RateLimit-Limit"))
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
}

func controlPortalSSOServer(t *testing.T, repo *httpSSORepoStub, now time.Time) nethttp.Handler {
	t.Helper()
	ssoSvc := service.NewSSOService(repo, service.SSOConfig{Now: func() time.Time { return now }})
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{SSO: ssoSvc}, HealthConfig{}, nil)
	require.NoError(t, err)
	return e
}

func serveControlSSO(server nethttp.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func userSSOContext(method, target, body, sessionToken string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if sessionToken != "" {
		req.AddCookie(&nethttp.Cookie{Name: service.SSOSessionCookieName, Value: sessionToken})
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func setUserSSOPrincipal(c echo.Context, profileID, teamID uuid.UUID) {
	ctx := httpmw.SetPrincipalForTest(c.Request().Context(), &httpmw.Principal{
		KeyID:      profileID,
		TeamID:     teamID,
		ProfileID:  &profileID,
		Scopes:     []string{service.APIKeyScopeRead},
		Role:       service.APIKeyRoleMember,
		AuthMethod: "sso_session",
	})
	c.SetRequest(c.Request().WithContext(ctx))
}

func setUserAPIKeyPrincipal(c echo.Context, profileID, teamID uuid.UUID) {
	ctx := httpmw.SetPrincipalForTest(c.Request().Context(), &httpmw.Principal{
		KeyID:      profileID,
		TeamID:     teamID,
		ProfileID:  &profileID,
		Scopes:     []string{service.APIKeyScopeRead},
		Role:       service.APIKeyRoleMember,
		AuthMethod: "api_key",
	})
	c.SetRequest(c.Request().WithContext(ctx))
}

func addSSOCSRF(c echo.Context, token string) {
	c.Request().Header.Set(service.SSOCSRFHeaderName, token)
	c.Request().AddCookie(&nethttp.Cookie{Name: service.SSOCSRFCookieName, Value: token})
}

type httpSSORepoStub struct {
	providers          map[uuid.UUID]*domain.SSOProvider
	providerList       []*domain.SSOProvider
	listProvidersErr   error
	listMappingsErr    error
	deleteProviderErr  error
	deleteMappingErr   error
	deletedProviderID  uuid.UUID
	cache              *domain.SSOEntitlementCache
	mappings           []*domain.SSOGroupMapping
	deletedMappingID   uuid.UUID
	identities         map[uuid.UUID]*domain.SSOIdentity
	teamProfiles       []*domain.SSOTeamProfile
	ssoProfiles        map[uuid.UUID]*domain.APIKey
	sessions           map[string]*domain.SSOSession
	updatedSessionHash string
	deletedSessionHash string
	deleteExpiredAt    time.Time
	now                time.Time
}

func (r *httpSSORepoStub) ListProviders(context.Context) ([]*domain.SSOProvider, error) {
	if r.listProvidersErr != nil {
		return nil, r.listProvidersErr
	}
	if len(r.providerList) > 0 {
		return copyHTTPSSOProviders(r.providerList), nil
	}
	items := make([]*domain.SSOProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		copy := *provider
		items = append(items, &copy)
	}
	return items, nil
}

func (r *httpSSORepoStub) ListEnabledProviders(ctx context.Context) ([]*domain.SSOProvider, error) {
	providers, err := r.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*domain.SSOProvider, 0, len(providers))
	for _, provider := range providers {
		if provider.Enabled {
			items = append(items, provider)
		}
	}
	return items, nil
}

func (r *httpSSORepoStub) GetProvider(_ context.Context, id uuid.UUID) (*domain.SSOProvider, error) {
	if r.providers == nil || r.providers[id] == nil {
		return nil, nil
	}
	copy := *r.providers[id]
	return &copy, nil
}

func (r *httpSSORepoStub) CreateProvider(_ context.Context, provider *domain.SSOProvider) error {
	if provider.ID == uuid.Nil {
		provider.ID = uuid.New()
	}
	r.stampProvider(provider)
	copy := *provider
	if r.providers == nil {
		r.providers = make(map[uuid.UUID]*domain.SSOProvider)
	}
	r.providers[provider.ID] = &copy
	r.providerList = append(r.providerList, &copy)
	return nil
}

func (r *httpSSORepoStub) UpdateProvider(_ context.Context, provider *domain.SSOProvider) error {
	r.stampProvider(provider)
	copy := *provider
	if r.providers == nil {
		r.providers = make(map[uuid.UUID]*domain.SSOProvider)
	}
	r.providers[provider.ID] = &copy
	for index, item := range r.providerList {
		if item.ID == provider.ID {
			r.providerList[index] = &copy
			return nil
		}
	}
	r.providerList = append(r.providerList, &copy)
	return nil
}

func (r *httpSSORepoStub) DeleteProvider(_ context.Context, id uuid.UUID) error {
	if r.deleteProviderErr != nil {
		return r.deleteProviderErr
	}
	r.deletedProviderID = id
	delete(r.providers, id)
	return nil
}

func (r *httpSSORepoStub) ListMappings(_ context.Context, providerID uuid.UUID) ([]*domain.SSOGroupMapping, error) {
	if r.listMappingsErr != nil {
		return nil, r.listMappingsErr
	}
	items := make([]*domain.SSOGroupMapping, 0)
	for _, mapping := range r.mappings {
		if mapping.ProviderID != providerID {
			continue
		}
		copy := *mapping
		copy.Scopes = append([]string(nil), mapping.Scopes...)
		items = append(items, &copy)
	}
	return items, nil
}

func (r *httpSSORepoStub) CreateMapping(_ context.Context, mapping *domain.SSOGroupMapping) error {
	if mapping.ID == uuid.Nil {
		mapping.ID = uuid.New()
	}
	r.stampMapping(mapping)
	copy := *mapping
	copy.Scopes = append([]string(nil), mapping.Scopes...)
	r.mappings = append(r.mappings, &copy)
	return nil
}

func (r *httpSSORepoStub) UpdateMapping(_ context.Context, mapping *domain.SSOGroupMapping) error {
	r.stampMapping(mapping)
	copy := *mapping
	copy.Scopes = append([]string(nil), mapping.Scopes...)
	for index, item := range r.mappings {
		if item.ID == mapping.ID {
			r.mappings[index] = &copy
			return nil
		}
	}
	r.mappings = append(r.mappings, &copy)
	return nil
}

func (r *httpSSORepoStub) DeleteMapping(_ context.Context, providerID, id uuid.UUID) error {
	if r.deleteMappingErr != nil {
		return r.deleteMappingErr
	}
	r.deletedMappingID = id
	for index, mapping := range r.mappings {
		if mapping.ID == id && mapping.ProviderID == providerID {
			r.mappings = append(r.mappings[:index], r.mappings[index+1:]...)
			return nil
		}
	}
	return nil
}

func (r *httpSSORepoStub) ListMappingsForGroups(_ context.Context, providerID uuid.UUID, groups []string) ([]*domain.SSOGroupMapping, error) {
	groupSet := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupSet[group] = struct{}{}
	}
	items := make([]*domain.SSOGroupMapping, 0)
	for _, mapping := range r.mappings {
		if mapping.ProviderID != providerID {
			continue
		}
		if _, ok := groupSet[mapping.GroupID]; !ok {
			continue
		}
		copy := *mapping
		copy.Scopes = append([]string(nil), mapping.Scopes...)
		items = append(items, &copy)
	}
	return items, nil
}

func (r *httpSSORepoStub) UpsertIdentity(_ context.Context, identity *domain.SSOIdentity) error {
	if identity.ID == uuid.Nil {
		identity.ID = uuid.New()
	}
	copy := *identity
	if r.identities == nil {
		r.identities = make(map[uuid.UUID]*domain.SSOIdentity)
	}
	r.identities[identity.ID] = &copy
	return nil
}

func (r *httpSSORepoStub) GetIdentity(_ context.Context, id uuid.UUID) (*domain.SSOIdentity, error) {
	if r.identities == nil || r.identities[id] == nil {
		return nil, nil
	}
	copy := *r.identities[id]
	return &copy, nil
}

func (r *httpSSORepoStub) GetIdentityByProviderSubject(context.Context, uuid.UUID, string) (*domain.SSOIdentity, error) {
	return nil, nil
}

func (r *httpSSORepoStub) UpsertTeamProfileForMapping(context.Context, domain.SSOIdentity, domain.SSOGroupMapping, string) (*domain.APIKey, error) {
	return nil, nil
}

func (r *httpSSORepoStub) ListTeamProfilesForIdentity(_ context.Context, identityID uuid.UUID) ([]*domain.SSOTeamProfile, error) {
	items := make([]*domain.SSOTeamProfile, 0, len(r.teamProfiles))
	for _, item := range r.teamProfiles {
		if item.Profile.SSOIdentityID == nil || *item.Profile.SSOIdentityID != identityID {
			continue
		}
		copy := *item
		copy.Profile.Scopes = append([]string(nil), item.Profile.Scopes...)
		items = append(items, &copy)
	}
	return items, nil
}

func (r *httpSSORepoStub) GetSSOProfileByID(_ context.Context, id uuid.UUID) (*domain.APIKey, error) {
	if r.ssoProfiles == nil || r.ssoProfiles[id] == nil {
		return nil, nil
	}
	copy := *r.ssoProfiles[id]
	copy.Scopes = append([]string(nil), r.ssoProfiles[id].Scopes...)
	return &copy, nil
}

func (r *httpSSORepoStub) GetEntitlementCache(_ context.Context, providerID uuid.UUID, subject string) (*domain.SSOEntitlementCache, error) {
	if r.cache == nil || r.cache.ProviderID != providerID || r.cache.Subject != subject {
		return nil, nil
	}
	copy := *r.cache
	copy.Groups = append([]string(nil), r.cache.Groups...)
	return &copy, nil
}

func (r *httpSSORepoStub) SetEntitlementCache(_ context.Context, cache domain.SSOEntitlementCache) error {
	copy := cache
	copy.Groups = append([]string(nil), cache.Groups...)
	r.cache = &copy
	return nil
}

func (r *httpSSORepoStub) CreateOAuthState(context.Context, domain.SSOOAuthState) error { return nil }
func (r *httpSSORepoStub) ConsumeOAuthState(context.Context, string) (*domain.SSOOAuthState, error) {
	return nil, nil
}
func (r *httpSSORepoStub) DeleteExpiredOAuthStates(_ context.Context, now time.Time) error {
	r.deleteExpiredAt = now
	return nil
}
func (r *httpSSORepoStub) CreateSession(context.Context, domain.SSOSession) error { return nil }

func (r *httpSSORepoStub) GetSession(_ context.Context, sessionHash string) (*domain.SSOSession, error) {
	if r.sessions == nil || r.sessions[sessionHash] == nil {
		return nil, nil
	}
	copy := *r.sessions[sessionHash]
	return &copy, nil
}

func (r *httpSSORepoStub) UpdateSessionTeam(_ context.Context, sessionHash string, teamProfileID, teamID uuid.UUID) error {
	r.updatedSessionHash = sessionHash
	if session := r.sessions[sessionHash]; session != nil {
		session.TeamProfileID = teamProfileID
		session.TeamID = teamID
	}
	return nil
}

func (r *httpSSORepoStub) DeleteSession(_ context.Context, sessionHash string) error {
	r.deletedSessionHash = sessionHash
	delete(r.sessions, sessionHash)
	return nil
}

func (r *httpSSORepoStub) DeleteExpiredSessions(context.Context, time.Time) error { return nil }

func (r *httpSSORepoStub) stampProvider(provider *domain.SSOProvider) {
	now := r.timestamp()
	if provider.CreatedAt.IsZero() {
		provider.CreatedAt = now
	}
	provider.UpdatedAt = now
}

func (r *httpSSORepoStub) stampMapping(mapping *domain.SSOGroupMapping) {
	now := r.timestamp()
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = now
	}
	mapping.UpdatedAt = now
}

func (r *httpSSORepoStub) timestamp() time.Time {
	if r.now.IsZero() {
		return time.Now().UTC()
	}
	return r.now
}

func httpSSOProvider(id uuid.UUID, name string, now time.Time) *domain.SSOProvider {
	return &domain.SSOProvider{
		ID:          id,
		Name:        name,
		Kind:        domain.SSOProviderKindGenericOIDC,
		IssuerURL:   "https://idp.example.com",
		ClientID:    "client-id",
		Scopes:      []string{"openid", "profile", "email"},
		GroupClaims: []string{"groups"},
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func httpSSODiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			nethttp.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `","authorization_endpoint":"` + server.URL + `/authorize","token_endpoint":"` + server.URL + `/token","jwks_uri":"` + server.URL + `/jwks"}`))
	}))
	return server
}

func httpSSOMapping(providerID, teamID uuid.UUID, teamName, groupID, role string, now time.Time) *domain.SSOGroupMapping {
	return &domain.SSOGroupMapping{
		ID:         uuid.New(),
		ProviderID: providerID,
		TeamID:     teamID,
		TeamName:   teamName,
		GroupID:    groupID,
		GroupName:  groupID,
		Scopes:     []string{service.APIKeyScopeRead, service.APIKeyScopeWrite},
		Role:       role,
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func httpSSOTeamProfile(identityID, providerID, profileID, teamID uuid.UUID, teamName, role string, now time.Time) *domain.SSOTeamProfile {
	return &domain.SSOTeamProfile{
		Team: domain.Profile{
			ID:        teamID,
			Name:      teamName,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Profile: domain.APIKey{
			ID:                   profileID,
			ProfileID:            teamID,
			TeamID:               teamID,
			TeamName:             teamName,
			Name:                 "SSO " + teamName,
			Scopes:               []string{service.APIKeyScopeRead, service.APIKeyScopeWrite},
			Role:                 role,
			CreatedAt:            now,
			AuthSource:           "sso",
			SSOIdentityID:        &identityID,
			SSOProviderID:        &providerID,
			SSOSubject:           "subject-123",
			SSOEmail:             "user@example.com",
			SSOEntitlementStatus: "active",
		},
	}
}

func copyHTTPSSOProviders(providers []*domain.SSOProvider) []*domain.SSOProvider {
	items := make([]*domain.SSOProvider, 0, len(providers))
	for _, provider := range providers {
		copy := *provider
		copy.Scopes = append([]string(nil), provider.Scopes...)
		copy.GroupClaims = append([]string(nil), provider.GroupClaims...)
		copy.GroupsScopes = append([]string(nil), provider.GroupsScopes...)
		items = append(items, &copy)
	}
	return items
}
