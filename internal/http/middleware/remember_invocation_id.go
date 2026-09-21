package middleware

import (
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

// RememberInvocationIDMiddleware installs a server-owned holder used by the
// Remember processor and completion-time transport logger.
func RememberInvocationIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := requestctx.WithRememberInvocationIDSink(c.Request().Context())
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
