package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/elimity-com/scim"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func TestDirectorySCIMRegistrationAndAuthenticationHelpers(t *testing.T) {
	if err := RegisterDirectorySCIM(nil, nil, DirectorySCIMConfig{}); err == nil {
		t.Fatal("nil SCIM server was accepted")
	}
	if err := RegisterDirectorySCIM(echo.New(), nil, DirectorySCIMConfig{}); err == nil {
		t.Fatal("nil directory service was accepted")
	}
	if _, err := newDirectorySCIMProtocolServer(nil, " https://scim.example/ "); err != nil {
		t.Fatalf("protocol server construction: %v", err)
	}
	for authorization, want := range map[string]bool{"Bearer token": true, "bearer token": true, "Bearer ": false, "Basic token": false, "": false} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(echo.HeaderAuthorization, authorization)
		_, ok := directoryBearerToken(req)
		if ok != want {
			t.Errorf("directoryBearerToken(%q) = %t, want %t", authorization, ok, want)
		}
	}
	if _, err := directorySCIMConnectorID(nil); err == nil {
		t.Fatal("nil SCIM request was accepted")
	}
	connectorID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(context.Background(), directorySCIMContextKey{}, connectorID))
	got, err := directorySCIMConnectorID(req)
	if err != nil || got != connectorID {
		t.Fatalf("directorySCIMConnectorID = %v, %v", got, err)
	}
}

func TestDirectorySCIMResourceHandlersRejectMissingConnectorContext(t *testing.T) {
	users := directorySCIMUserResourceHandler{}
	groups := directorySCIMGroupResourceHandler{}
	attributes := scim.ResourceAttributes{}
	params := scim.ListRequestParams{}
	if _, err := users.Create(nil, attributes); err == nil {
		t.Fatal("user create without connector was accepted")
	}
	if _, err := users.Get(nil, "bad"); err == nil {
		t.Fatal("user get without connector was accepted")
	}
	if _, err := users.GetAll(nil, params); err == nil {
		t.Fatal("user list without connector was accepted")
	}
	if _, err := users.Replace(nil, "bad", attributes); err == nil {
		t.Fatal("user replace without connector was accepted")
	}
	if err := users.Delete(nil, "bad"); err == nil {
		t.Fatal("user delete without connector was accepted")
	}
	if _, err := users.Patch(nil, "bad", nil); err == nil {
		t.Fatal("user patch without connector was accepted")
	}
	if _, err := groups.Create(nil, attributes); err == nil {
		t.Fatal("group create without connector was accepted")
	}
	if _, err := groups.Get(nil, "bad"); err == nil {
		t.Fatal("group get without connector was accepted")
	}
	if _, err := groups.GetAll(nil, params); err == nil {
		t.Fatal("group list without connector was accepted")
	}
	if _, err := groups.Replace(nil, "bad", attributes); err == nil {
		t.Fatal("group replace without connector was accepted")
	}
	if err := groups.Delete(nil, "bad"); err == nil {
		t.Fatal("group delete without connector was accepted")
	}
	if _, err := groups.Patch(nil, "bad", nil); err == nil {
		t.Fatal("group patch without connector was accepted")
	}
	if _, _, err := directorySCIMResourceIDs(nil, "bad"); err == nil {
		t.Fatal("nil resource request was accepted")
	}
}

func TestDirectorySCIMOAuthAndErrorHelpers(t *testing.T) {
	e := echo.New()
	h := &directorySCIMHandler{}
	for name, req := range map[string]*http.Request{
		"bad grant":     httptest.NewRequest(http.MethodPost, "/scim/oauth/token?grant_type=password", nil),
		"missing grant": httptest.NewRequest(http.MethodPost, "/scim/oauth/token", nil),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := e.NewContext(req, httptest.NewRecorder())
			err := h.oauthToken(ctx)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadRequest, ctx.Response().Status)
		})
	}
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	require.NoError(t, directoryOAuthError(ctx, http.StatusUnauthorized, "invalid_client"))
	require.Equal(t, "no-cache", ctx.Response().Header().Get("Pragma"))
	recordDirectoryOAuthAuthFailure(ctx, nil)
	if err := directorySCIMMutationError(accessservice.ErrDirectoryConnectorDisabled); err == nil {
		t.Fatal("directory mutation error was lost")
	}
}

func TestDirectorySCIMServeRejectsMalformedAuthentication(t *testing.T) {
	directory := accessservice.NewDirectoryIdentityService(nil, accessservice.DirectoryIdentityConfig{})
	h := &directorySCIMHandler{directory: directory}
	e := echo.New()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("connectorId", "*")
	ctx.SetParamValues("not-a-uuid", "Users")
	if err := h.serve(ctx); err != nil {
		t.Fatal(err)
	}
	ctx = e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	ctx.SetParamNames("connectorId", "*")
	ctx.SetParamValues(uuid.NewString(), "Users")
	if err := h.serve(ctx); err != nil {
		t.Fatal(err)
	}
}
