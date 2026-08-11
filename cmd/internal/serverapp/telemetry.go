package serverapp

import (
	"context"
	"fmt"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
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
	e.GET("/metrics", echo.WrapHandler(scrapeHandler), httpmw.TelemetryScrapeTokenMiddleware(scrapeToken))
	return e, nil
}

func shutdownTelemetryScrapeServer(e *echo.Echo) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return e.Shutdown(ctx)
}
