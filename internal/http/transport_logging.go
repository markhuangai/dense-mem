package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/markhuangai/dense-mem/internal/domain"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func transportRequestAttrs(c echo.Context, values middleware.RequestLoggerValues) []httpcontract.LogAttr {
	uri := "/unmatched"
	if values.RoutePath != "" {
		uri = values.RoutePath
	}
	attrs := []httpcontract.LogAttr{
		httpcontract.String("method", values.Method),
		httpcontract.String("uri", uri),
		httpcontract.Int("status", values.Status),
		httpcontract.Int("duration_ms", int(values.Latency.Milliseconds())),
		httpcontract.String("transport_status", requestTransportStatus(values)),
		httpcontract.String("caller_receipt", "unknown"),
	}
	if values.RoutePath != "" {
		attrs = append(attrs, httpcontract.String("route", values.RoutePath))
	}
	ctx := context.Background()
	if c != nil && c.Request() != nil {
		ctx = c.Request().Context()
	}
	metrics := domain.MCPToolMetricsFromContext(ctx)
	calls, failures := metrics.Snapshot()
	if calls > 0 || failures > 0 {
		attrs = append(attrs,
			httpcontract.Int("mcp_tool_calls", int(calls)),
			httpcontract.Int("mcp_tool_failures", int(failures)),
		)
		outcome := "tool_success"
		if failures > 0 {
			outcome = "tool_error"
		}
		attrs = append(attrs, httpcontract.String("application_outcome", outcome))
	}
	observer := deliveryObserverFromContext(c)
	attrs = append(attrs,
		httpcontract.String("delivery_stage", requestDeliveryStage(c, values, observer)),
		httpcontract.Int("write_bytes", int(observerWriteBytes(observer))),
	)
	if invocationID := requestctx.RememberInvocationIDFromContext(ctx); invocationID != "" {
		attrs = append(attrs, httpcontract.String("invocation_id", invocationID))
	}
	return attrs
}

// TransportLogContext keeps request values available to completion logging
// after the client has canceled the request or its deadline has elapsed.
func TransportLogContext(c echo.Context) context.Context {
	if c == nil || c.Request() == nil {
		return context.Background()
	}
	return context.WithoutCancel(c.Request().Context())
}

// TransportRequestAttrs exposes the bounded completion fields to the private
// metrics listener, which shares the same root-backed transport contract.
func TransportRequestAttrs(c echo.Context, values middleware.RequestLoggerValues) []httpcontract.LogAttr {
	return transportRequestAttrs(c, values)
}

func requestTransportStatus(values middleware.RequestLoggerValues) string {
	status := transportStatus(values.Status)
	if values.Error != nil && values.Status < http.StatusBadRequest {
		return "error"
	}
	return status
}

func requestDeliveryStage(c echo.Context, values middleware.RequestLoggerValues, observer *DeliveryObservation) string {
	if observer != nil {
		ctx := context.Background()
		if c != nil && c.Request() != nil {
			ctx = c.Request().Context()
		}
		return observer.Stage(ctx, values.Error)
	}
	if c != nil && c.Request() != nil {
		if err := c.Request().Context().Err(); errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "disconnect_observed"
		}
	}
	if errors.Is(values.Error, context.Canceled) || errors.Is(values.Error, context.DeadlineExceeded) {
		return "disconnect_observed"
	}
	if values.Error != nil && c != nil && c.Response() != nil && c.Response().Committed {
		return "write_error_observed"
	}
	if c != nil && c.Response() != nil && c.Response().Committed {
		return "write_observed"
	}
	return "prepared"
}

func observerWriteBytes(observer *DeliveryObservation) int64 {
	if observer == nil {
		return 0
	}
	return observer.WriteBytes()
}
