package serverapp

import (
	"context"
	"crypto/subtle"
	"fmt"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"github.com/markhuangai/dense-mem/internal/httperr"
)

func newTelemetryScrapeServer(scrapeHandler nethttp.Handler, scrapeToken string) (*echo.Echo, error) {
	if scrapeHandler == nil {
		return nil, fmt.Errorf("telemetry scrape handler is required")
	}
	if strings.TrimSpace(scrapeToken) == "" {
		return nil, fmt.Errorf("telemetry scrape token is required")
	}

	e := echo.New()
	e.Server.ReadHeaderTimeout = 5 * time.Second
	e.Server.ReadTimeout = 30 * time.Second
	e.Server.IdleTimeout = 60 * time.Second
	e.IPExtractor = echo.ExtractIPFromXFFHeader()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(echomw.Recover())
	e.GET("/metrics", echo.WrapHandler(scrapeHandler), telemetryScrapeTokenMiddleware(scrapeToken))
	return e, nil
}

func shutdownTelemetryScrapeServer(e *echo.Echo) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return e.Shutdown(ctx)
}

func telemetryScrapeTokenMiddleware(token string) echo.MiddlewareFunc {
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
