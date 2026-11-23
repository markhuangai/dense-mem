package serverapp

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"

	densehttp "github.com/markhuangai/dense-mem/internal/http"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools"
)

func newTelemetryScrapeServer(scrapeHandler nethttp.Handler, scrapeToken string, loggers ...httpcontract.LogProvider) (*nethttp.Server, error) {
	if scrapeHandler == nil {
		return nil, fmt.Errorf("telemetry scrape handler is required")
	}
	if strings.TrimSpace(scrapeToken) == "" {
		return nil, fmt.Errorf("telemetry scrape token is required")
	}

	var logger httpcontract.LogProvider
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	e := echo.NewWithConfig(echo.Config{
		Logger:           densehttp.NewEchoLogger(logger),
		HTTPErrorHandler: httperr.ErrorHandler,
		IPExtractor:      echo.ExtractIPFromXFFHeader(),
	})
	e.Use(densehttp.DeliveryObservationMiddleware)
	e.Use(echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		HandleError:  true,
		LogMethod:    true,
		LogURI:       true,
		LogStatus:    true,
		LogLatency:   true,
		LogRoutePath: true,
		LogValuesFunc: func(c *echo.Context, values echomw.RequestLoggerValues) error {
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
	return densehttp.NewHTTPServer(e, logger), nil
}

func shutdownTelemetryScrapeServer(server *nethttp.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}
