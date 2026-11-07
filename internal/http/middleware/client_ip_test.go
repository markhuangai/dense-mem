package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestClientIPMiddlewareStoresRealIPInContext(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteAddr string
		extractor  echo.IPExtractor
		wantIP     string
	}{
		{"direct_peer", "192.0.2.1:12345", nil, "192.0.2.1"},
		{"configured_trusted_proxy", "127.0.0.1:12345", echo.ExtractIPFromXFFHeader(), "203.0.113.10"},
		{"configured_untrusted_peer", "192.0.2.1:12345", echo.ExtractIPFromXFFHeader(), "192.0.2.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			e.IPExtractor = tc.extractor
			e.Use(ClientIPMiddleware())
			e.GET("/test", func(c echo.Context) error {
				got := requestctx.ClientIPFromContext(c.Request().Context())
				if got != tc.wantIP {
					t.Errorf("ClientIPFromContext() = %q; want %q", got, tc.wantIP)
				}
				return c.NoContent(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set(echo.HeaderXForwardedFor, "203.0.113.10")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Errorf("status = %d; want %d", rec.Code, http.StatusNoContent)
			}
		})
	}
}
