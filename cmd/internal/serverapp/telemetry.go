package serverapp

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	densehttp "github.com/markhuangai/dense-mem/internal/http"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools"
)

func newTelemetryScrapeServer(scrapeHandler nethttp.Handler, scrapeToken string, loggers ...httpcontract.LogProvider) (*echo.Echo, error) {
	if scrapeHandler == nil {
		return nil, fmt.Errorf("telemetry scrape handler is required")
	}
	if strings.TrimSpace(scrapeToken) == "" {
		return nil, fmt.Errorf("telemetry scrape token is required")
	}

	e := echo.New()
	var logger httpcontract.LogProvider
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	if rootLogger := densehttp.NewEchoLogger(logger); rootLogger != nil {
		e.Logger = rootLogger
		e.StdLogger = densehttp.EchoServerErrorLogger(logger)
	}
	e.Server.ReadHeaderTimeout = 5 * time.Second
	e.Server.ReadTimeout = 30 * time.Second
	e.Server.IdleTimeout = 60 * time.Second
	e.IPExtractor = echo.ExtractIPFromXFFHeader()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(densehttp.DeliveryObservationMiddleware)
	e.Use(echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		HandleError:  true,
		LogMethod:    true,
		LogURI:       true,
		LogStatus:    true,
		LogLatency:   true,
		LogError:     true,
		LogRoutePath: true,
		LogValuesFunc: func(c echo.Context, values echomw.RequestLoggerValues) error {
			if logger == nil {
				return nil
			}
			attrs := densehttp.TransportRequestAttrs(c, values)
			if values.Error != nil {
				httpcontract.LogErrorContext(densehttp.TransportLogContext(c), logger, "telemetry_http_request", errors.New(tools.SanitizeError(values.Error)), attrs...)
				return nil
			}
			if values.Status >= nethttp.StatusBadRequest {
				httpcontract.LogWarnContext(densehttp.TransportLogContext(c), logger, "telemetry_http_request", attrs...)
				return nil
			}
			httpcontract.LogInfoContext(densehttp.TransportLogContext(c), logger, "telemetry_http_request", attrs...)
			return nil
		},
	}))
	e.Use(densehttp.RootRecover(logger))
	e.Use(httpmw.CorrelationIDMiddleware())
	e.GET("/metrics", echo.WrapHandler(scrapeHandler), httpmw.TelemetryScrapeTokenMiddleware(scrapeToken))
	return e, nil
}

func shutdownTelemetryScrapeServer(e *echo.Echo) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return e.Shutdown(ctx)
}
