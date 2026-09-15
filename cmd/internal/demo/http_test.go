package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/cmd/internal/demo/service"
	"github.com/stretchr/testify/require"
)

type demoProvisionerStub struct {
	response *service.ProvisionResponse
	err      error
	options  service.ProvisionOptions
}

func (s *demoProvisionerStub) Provision(_ context.Context, options service.ProvisionOptions) (*service.ProvisionResponse, error) {
	s.options = options
	return s.response, s.err
}

func TestRegisterRoutesUsesUserPortalAPIPrefix(t *testing.T) {
	e := echo.New()
	RegisterRoutes(e, nil, "")

	paths := map[string]struct{}{}
	for _, route := range e.Routes() {
		if route.Method == http.MethodPost {
			paths[route.Path] = struct{}{}
		}
	}
	require.Contains(t, paths, "/ui/api/demo/session")
	require.NotContains(t, paths, "/demo/api/session")
}

func TestDemoRequestBaseURLAndHeaderHelpers(t *testing.T) {
	if got := firstHeader(" first, second "); got != "first" {
		t.Fatalf("firstHeader = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "http://internal.test/", nil)
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Header.Set("X-Forwarded-Host", "public.test, internal.test")
	ctx := echo.New().NewContext(req, httptest.NewRecorder())
	if got := requestBaseURL(ctx, ""); got != "https://public.test" {
		t.Fatalf("forwarded base URL = %q", got)
	}
	if got := requestBaseURL(ctx, " https://configured.test/ "); got != "https://configured.test" {
		t.Fatalf("configured base URL = %q", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "fallback.test"
	ctx = echo.New().NewContext(req, httptest.NewRecorder())
	if got := requestBaseURL(ctx, ""); got != "http://fallback.test" {
		t.Fatalf("fallback base URL = %q", got)
	}
}

func TestDemoRoutesServeLandingAndProvisionResponse(t *testing.T) {
	provisioner := &demoProvisionerStub{response: &service.ProvisionResponse{APIKey: "key", ExpiresAt: time.Now().UTC()}}
	e := echo.New()
	RegisterRoutes(e, provisioner, "https://demo.test")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "Dense-Mem Demo")
	request = httptest.NewRequest(http.MethodPost, "/ui/api/demo/session", strings.NewReader(""))
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "public.test")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code)
	require.Equal(t, "https://demo.test", provisioner.options.BaseURL)
	var body service.ProvisionResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "key", body.APIKey)
}
