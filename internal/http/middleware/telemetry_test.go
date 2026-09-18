package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestTelemetryScrapeTokenMiddlewareRetainsPresentedSecretOnFailure(t *testing.T) {
	var observed context.Context
	e := echo.New()
	e.HTTPErrorHandler = func(_ error, c echo.Context) {
		observed = c.Request().Context()
		_ = c.NoContent(http.StatusUnauthorized)
	}
	e.Use(TelemetryScrapeTokenMiddleware("expected"))
	e.GET("/metrics", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("X-Telemetry-Scrape-Token", "presented-scrape-token")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.NotNil(t, observed)
	require.Equal(t, []string{"presented-scrape-token"}, requestctx.AuthenticationSecretsFromContext(observed))
}

func TestTelemetryScrapeTokenMiddlewareMarksVerifiedToken(t *testing.T) {
	var observed context.Context
	e := echo.New()
	e.Use(TelemetryScrapeTokenMiddleware("expected"))
	e.GET("/metrics", func(c echo.Context) error {
		observed = c.Request().Context()
		return c.NoContent(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("X-Telemetry-Scrape-Token", "expected")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.True(t, requestctx.AuthenticationVerifiedFromContext(observed))
}
