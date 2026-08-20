package http

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestOAuthProtectedResourceMetadataRoutes(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	provider := &oauthMetadataStub{
		baseURL: "https://memory.example.test/",
		metadata: service.OAuthProtectedResourceMetadata{
			AuthorizationServers: []string{"https://idp.example.test"},
			ScopesSupported:      []string{"densemem.read", "densemem.write"},
		},
	}
	RegisterOAuthProtectedResourceRoutes(e, provider)

	t.Run("unscoped", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
		require.Equal(t, nethttp.StatusOK, recorder.Code)
		var body oauthProtectedResourceDocument
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.Equal(t, "https://memory.example.test/mcp", body.Resource)
		require.Equal(t, provider.metadata.AuthorizationServers, body.AuthorizationServers)
		require.Equal(t, provider.metadata.ScopesSupported, body.ScopesSupported)
		require.Equal(t, []string{"header"}, body.BearerMethods)
		require.Equal(t, "no-store", recorder.Header().Get(echo.HeaderCacheControl))
	})

	t.Run("team scoped", func(t *testing.T) {
		teamID := uuid.New()
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/teams/"+teamID.String()+"/mcp", nil))
		require.Equal(t, nethttp.StatusOK, recorder.Code)
		var body oauthProtectedResourceDocument
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.Equal(t, "https://memory.example.test/teams/"+teamID.String()+"/mcp", body.Resource)
	})

	t.Run("invalid team", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/teams/not-a-uuid/mcp", nil))
		require.Equal(t, nethttp.StatusBadRequest, recorder.Code)
	})

	t.Run("configured base path", func(t *testing.T) {
		provider.baseURL = "https://memory.example.test/base/"
		teamID := uuid.New()
		for _, test := range []struct {
			path     string
			resource string
		}{
			{path: "/.well-known/oauth-protected-resource/base/mcp", resource: "https://memory.example.test/base/mcp"},
			{path: "/.well-known/oauth-protected-resource/base/teams/" + teamID.String() + "/mcp", resource: "https://memory.example.test/base/teams/" + teamID.String() + "/mcp"},
		} {
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, test.path, nil))
			require.Equal(t, nethttp.StatusOK, recorder.Code)
			var body oauthProtectedResourceDocument
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Equal(t, test.resource, body.Resource)
		}

		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/wrong/mcp", nil))
		require.Equal(t, nethttp.StatusNotFound, recorder.Code)
	})
}

func TestOAuthProtectedResourceMetadataIsDormantWithoutEnabledProvider(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	RegisterOAuthProtectedResourceRoutes(e, &oauthMetadataStub{})
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, nethttp.StatusNotFound, recorder.Code)
}

func TestOAuthProtectedResourceChallengeUsesMatchingMetadataDocument(t *testing.T) {
	provider := &oauthMetadataStub{
		baseURL:  "https://memory.example.test",
		metadata: service.OAuthProtectedResourceMetadata{AuthorizationServers: []string{"https://idp.example.test"}},
	}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	RegisterProtectedRoutesWithHandlers(e, ProtectedDeps{OAuthMetadata: provider}, ProtectedHandlers{
		MCPGet: func(c echo.Context) error { return c.NoContent(nethttp.StatusOK) },
	})

	tests := []struct {
		name      string
		baseURL   string
		path      string
		challenge string
	}{
		{name: "root unscoped", baseURL: "https://memory.example.test", path: "/mcp", challenge: `Bearer resource_metadata="https://memory.example.test/.well-known/oauth-protected-resource/mcp"`},
		{name: "root team scoped", baseURL: "https://memory.example.test", path: "/teams/11111111-1111-4111-8111-111111111111/mcp", challenge: `Bearer resource_metadata="https://memory.example.test/.well-known/oauth-protected-resource/teams/11111111-1111-4111-8111-111111111111/mcp"`},
		{name: "prefixed unscoped", baseURL: "https://memory.example.test/base", path: "/mcp", challenge: `Bearer resource_metadata="https://memory.example.test/.well-known/oauth-protected-resource/base/mcp"`},
		{name: "prefixed team scoped", baseURL: "https://memory.example.test/base", path: "/teams/11111111-1111-4111-8111-111111111111/mcp", challenge: `Bearer resource_metadata="https://memory.example.test/.well-known/oauth-protected-resource/base/teams/11111111-1111-4111-8111-111111111111/mcp"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider.baseURL = test.baseURL
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, test.path, nil))
			require.Equal(t, nethttp.StatusUnauthorized, recorder.Code)
			require.Equal(t, test.challenge, recorder.Header().Get(echo.HeaderWWWAuthenticate))
		})
	}
}

func TestOAuthProtectedResourceMetadataRequiresConfiguredOrigin(t *testing.T) {
	provider := &oauthMetadataStub{
		metadata: service.OAuthProtectedResourceMetadata{
			AuthorizationServers: []string{"https://idp.example.test"},
		},
	}
	RegisterOAuthProtectedResourceRoutes(nil, provider)
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	RegisterOAuthProtectedResourceRoutes(e, nil)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, nethttp.StatusNotFound, recorder.Code)

	RegisterOAuthProtectedResourceRoutes(e, provider)
	provider.metadataErr = errors.New("metadata unavailable")
	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, nethttp.StatusServiceUnavailable, recorder.Code)
	provider.metadataErr = nil

	provider.baseErr = errors.New("config unavailable")
	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "https://fallback.example.test/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, nethttp.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	require.Empty(t, recorder.Header().Get(echo.HeaderCacheControl))

	request := httptest.NewRequest(nethttp.MethodGet, "https://fallback.example.test/.well-known/oauth-protected-resource/mcp", nil)
	request.Host = "attacker.example.test"
	provider.baseErr = nil
	provider.baseURL = "https://memory.example.test/base"
	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, request)
	require.Equal(t, nethttp.StatusOK, recorder.Code)
	var body oauthProtectedResourceDocument
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "https://memory.example.test/base/mcp", body.Resource)

	provider.baseURL = "ftp://memory.example.test"
	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, nethttp.StatusServiceUnavailable, recorder.Code)
}

func TestOAuthProtectedResourceChallengeSkipsNonOAuthFailureSurfaces(t *testing.T) {
	provider := &oauthMetadataStub{
		baseURL: "https://memory.example.test",
		metadata: service.OAuthProtectedResourceMetadata{
			AuthorizationServers: []string{"https://idp.example.test"},
		},
	}
	tests := []struct {
		name     string
		provider OAuthProtectedResourceProvider
		path     string
		next     echo.HandlerFunc
	}{
		{
			name:     "successful handler",
			provider: provider,
			next:     func(echo.Context) error { return nil },
		},
		{
			name: "provider absent",
			next: func(echo.Context) error { return httperr.New(httperr.AUTH_MISSING, "unauthorized") },
		},
		{
			name:     "non unauthorized api error",
			provider: provider,
			next:     func(echo.Context) error { return httperr.New(httperr.FORBIDDEN, "forbidden") },
		},
		{
			name:     "invalid scoped team",
			provider: provider,
			path:     "/teams/:teamId/mcp",
			next:     func(echo.Context) error { return echo.NewHTTPError(nethttp.StatusUnauthorized) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(nethttp.MethodGet, "/teams/invalid/mcp", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath(test.path)
			if test.path != "" {
				c.SetParamNames("teamId")
				c.SetParamValues("invalid")
			}
			err := oauthProtectedResourceChallenge(test.provider)(test.next)(c)
			if test.name == "successful handler" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Empty(t, rec.Header().Get(echo.HeaderWWWAuthenticate))
		})
	}

	provider.metadataErr = errors.New("metadata unavailable")
	e := echo.New()
	req := httptest.NewRequest(nethttp.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/mcp")
	err := oauthProtectedResourceChallenge(provider)(func(echo.Context) error {
		return echo.NewHTTPError(nethttp.StatusUnauthorized)
	})(c)
	require.Error(t, err)
	require.Empty(t, rec.Header().Get(echo.HeaderWWWAuthenticate))

	provider.metadataErr = nil
	provider.metadata.AuthorizationServers = nil
	err = oauthProtectedResourceChallenge(provider)(func(echo.Context) error {
		return echo.NewHTTPError(nethttp.StatusUnauthorized)
	})(c)
	require.Error(t, err)
	require.Empty(t, rec.Header().Get(echo.HeaderWWWAuthenticate))

	provider.metadata.AuthorizationServers = []string{"https://idp.example.test"}
	provider.baseURL = ""
	provider.baseErr = errors.New("base URL unavailable")
	req = httptest.NewRequest(nethttp.MethodGet, "/mcp", nil)
	req.Host = ""
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetPath("/mcp")
	err = oauthProtectedResourceChallenge(provider)(func(echo.Context) error {
		return echo.NewHTTPError(nethttp.StatusUnauthorized)
	})(c)
	require.Error(t, err)
	require.Empty(t, rec.Header().Get(echo.HeaderWWWAuthenticate))

	require.Equal(t, nethttp.StatusInternalServerError, oauthErrorStatus(errors.New("unknown")))
	require.Equal(t, nethttp.StatusUnauthorized, oauthErrorStatus(echo.NewHTTPError(nethttp.StatusUnauthorized)))
}

type oauthMetadataStub struct {
	baseURL     string
	metadata    service.OAuthProtectedResourceMetadata
	metadataErr error
	baseErr     error
}

func (s *oauthMetadataStub) OAuthProtectedResourceMetadata(context.Context) (service.OAuthProtectedResourceMetadata, error) {
	return s.metadata, s.metadataErr
}

func (s *oauthMetadataStub) MCPPublicBaseURL(context.Context) (string, error) {
	return s.baseURL, s.baseErr
}
