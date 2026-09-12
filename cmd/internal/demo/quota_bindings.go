package demo

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	demoservice "github.com/markhuangai/dense-mem/cmd/internal/demo/service"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func RequestQuotaMiddleware(manager *demoservice.QuotaManager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			teamID := ""
			if actor, ok := requestctx.ActorFromContext(c.Request().Context()); ok && actor.TeamID != uuid.Nil {
				teamID = actor.TeamID.String()
			}
			if teamID == "" {
				if resolved, ok := httpmw.GetResolvedTeamID(c.Request().Context()); ok && resolved != uuid.Nil {
					teamID = resolved.String()
				}
			}
			if teamID == "" {
				if principal := httpmw.GetPrincipal(c.Request().Context()); principal != nil && principal.GetTeamID() != uuid.Nil {
					teamID = principal.GetTeamID().String()
				}
			}
			if teamID != "" {
				if err := manager.ConsumeRequest(c.Request().Context(), teamID); err != nil {
					return err
				}
			}
			return next(c)
		}
	}
}

func WrapRegistry(reg registry.Registry, manager *demoservice.QuotaManager) (registry.Registry, error) {
	wrapped := registry.New()
	for _, tool := range reg.List() {
		invoke := tool.Invoke
		name := tool.Name
		if invoke != nil {
			tool.Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
				teamID = toolTeamID(ctx, teamID)
				if err := manager.ConsumeTool(ctx, teamID, name, input); err != nil {
					return nil, err
				}
				return invoke(ctx, teamID, input)
			}
		}
		if err := wrapped.Register(tool); err != nil {
			return nil, err
		}
	}
	return wrapped, nil
}

func toolTeamID(ctx context.Context, teamID string) string {
	if actor, ok := requestctx.ActorFromContext(ctx); ok && actor.TeamID != uuid.Nil {
		return actor.TeamID.String()
	}
	return strings.TrimSpace(teamID)
}
