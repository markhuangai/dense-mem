package middleware

import (
	"crypto/subtle"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
)

// TelemetryScrapeTokenMiddleware authenticates the dedicated Prometheus
// endpoint using either its explicit header or a bearer authorization header.
func TelemetryScrapeTokenMiddleware(token string) echo.MiddlewareFunc {
	expected := strings.TrimSpace(token)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			got := c.Request().Header.Get("X-Telemetry-Scrape-Token")
			if got == "" {
				auth := c.Request().Header.Get(echo.HeaderAuthorization)
				if strings.HasPrefix(auth, "Bearer ") {
					got = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
				return httperr.New(httperr.AUTH_INVALID, "invalid telemetry scrape token")
			}
			return next(c)
		}
	}
}
