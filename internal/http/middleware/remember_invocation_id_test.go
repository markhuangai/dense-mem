package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRememberInvocationIDMiddlewareInstallsSink(t *testing.T) {
	e := echo.New()
	e.Use(RememberInvocationIDMiddleware())
	e.GET("/", func(c echo.Context) error {
		requestctx.SetRememberInvocationID(c.Request().Context(), "invocation-1")
		return c.NoContent(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
}
