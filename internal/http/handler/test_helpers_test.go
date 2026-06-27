package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/sse"
)

func injectProfileMiddleware(profileID uuid.UUID) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = middleware.SetResolvedProfileIDForTest(ctx, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

type mockStreamLifecycle struct {
	startFunc func(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error
}

func (m *mockStreamLifecycle) Start(ctx context.Context, profileID string, writer sse.SSEWriter, work func(context.Context) error) error {
	if m.startFunc != nil {
		return m.startFunc(ctx, profileID, writer, work)
	}
	return work(ctx)
}
