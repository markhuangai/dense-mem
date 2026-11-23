package middleware

import (
	"github.com/labstack/echo/v5"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// ClientIPMiddleware stores Echo's resolved client IP in the request context.
func ClientIPMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ctx := requestctx.WithClientIP(c.Request().Context(), c.RealIP())
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
