package middleware

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/correlation"
)

// CorrelationIDHeader is the HTTP header for correlation IDs.
const CorrelationIDHeader = "X-Correlation-ID"

// CorrelationIDMiddleware reads or generates a correlation ID for each request.
// It reads X-Correlation-ID from the request header if present,
// otherwise generates a new UUID. The correlation ID is stored in the
// request context and echoed back in the response header.
func CorrelationIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := c.Request().Header.Get(CorrelationIDHeader)
			if id == "" {
				id = uuid.New().String()
			}

			ctx := correlation.WithClientProvidedID(c.Request().Context(), id)
			if c.Request().Header.Get(CorrelationIDHeader) == "" {
				ctx = correlation.WithID(c.Request().Context(), id)
			}
			c.SetRequest(c.Request().WithContext(ctx))

			c.Response().Header().Set(CorrelationIDHeader, id)

			return next(c)
		}
	}
}

// GetCorrelationID retrieves the correlation ID from the context.
// Returns empty string if not found. Provided for the existing handler call sites.
func GetCorrelationID(ctx context.Context) string {
	return correlation.FromContext(ctx)
}
