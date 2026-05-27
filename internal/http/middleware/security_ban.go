package middleware

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

type SecurityBanService interface {
	CheckBan(ctx context.Context, ip string) (*domain.SecurityIPBan, error)
	RecordAuthFailure(ctx context.Context, ip, surface, reason string) (*domain.SecurityIPBan, error)
}

func SecurityBanMiddleware(svc SecurityBanService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if svc == nil {
				return next(c)
			}
			ban, err := svc.CheckBan(c.Request().Context(), c.RealIP())
			if err != nil {
				c.Logger().Errorf("security ban check failed: %v", err)
				return next(c)
			}
			if ban != nil {
				return httperr.New(httperr.FORBIDDEN, "client blocked")
			}
			return next(c)
		}
	}
}
