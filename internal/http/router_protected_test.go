package http

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type routerAPIKeyService struct{}

func (routerAPIKeyService) CreateStandardKey(context.Context, uuid.UUID, service.CreateAPIKeyRequest, *string, string, string, string) (*domain.APIKey, string, error) {
	return nil, "", nil
}

func (routerAPIKeyService) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}

func (routerAPIKeyService) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, service.CreateAPIKeyRequest, *string, string, string, string) (*domain.APIKey, string, error) {
	return nil, "", nil
}

func (routerAPIKeyService) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	return nil, nil
}

func (routerAPIKeyService) CountByProfile(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}

func (routerAPIKeyService) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (routerAPIKeyService) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}

func TestProtectedDepsGettersReturnConfiguredDependencies(t *testing.T) {
	deps := &ProtectedDeps{
		APIKeyRepo:       nil,
		ProfileService:   nil,
		ProfileSvc:       nil,
		RateLimitService: nil,
		UsageMetrics:     nil,
		AuditService:     nil,
		SecurityService:  nil,
		Config:           nil,
		Logger:           nil,
	}

	if deps.GetAPIKeyRepo() != nil ||
		deps.GetProfileService() != nil ||
		deps.GetProfileSvc() != nil ||
		deps.GetRateLimitService() != nil ||
		deps.GetUsageMetrics() != nil ||
		deps.GetAuditService() != nil ||
		deps.GetSecurityService() != nil ||
		deps.GetConfig() != nil ||
		deps.GetLogger() != nil {
		t.Fatal("zero-value ProtectedDeps getters must return configured nil dependencies")
	}
}

func TestRegisterProtectedRoutesWithHandlersRegistersConfiguredSurface(t *testing.T) {
	e := echo.New()
	noContent := func(c echo.Context) error { return c.NoContent(204) }

	RegisterProtectedRoutesWithHandlers(e, ProtectedDeps{}, ProtectedHandlers{
		GetTool:         noContent,
		ExecuteTool:     noContent,
		GraphQuery:      noContent,
		KeywordSearch:   noContent,
		SemanticSearch:  noContent,
		QueryStream:     noContent,
		FragmentCreate:  noContent,
		FragmentRead:    noContent,
		FragmentList:    noContent,
		FragmentDelete:  noContent,
		FragmentRetract: noContent,
		ToolCatalog:     noContent,
		OpenAPIFull:     noContent,
		MCPPost:         noContent,
		MCPGet:          noContent,
		APIKeySvc:       routerAPIKeyService{},
		ClaimCreate:     noContent,
		ClaimRead:       noContent,
		ClaimList:       noContent,
		ClaimDelete:     noContent,
		ClaimVerify:     noContent,
		ClaimPromote:    noContent,
		FactGet:         noContent,
		FactList:        noContent,
		FactRetract:     noContent,
		CommunityRead:   noContent,
		CommunityList:   noContent,
		Recall:          noContent,
	})

	routes := registeredRoutes(e)
	required := []string{
		"GET /api/v1/teams/:teamId",
		"PATCH /api/v1/teams/:teamId",
		"GET /api/v1/teams/:teamId/audit-log",
		"POST /api/v1/teams/:teamId/query/stream",
		"GET /api/v1/teams/:teamId/profiles",
		"POST /api/v1/teams/:teamId/profiles",
		"GET /api/v1/teams/:teamId/profiles/:profileId",
		"POST /api/v1/teams/:teamId/profiles/:profileId/rotate",
		"DELETE /api/v1/teams/:teamId/profiles/:profileId",
		"GET /api/v1/profiles/:profileId",
		"PATCH /api/v1/profiles/:profileId",
		"GET /api/v1/profiles/:profileId/api-keys",
		"POST /api/v1/profiles/:profileId/api-keys",
		"GET /api/v1/profiles/:profileId/api-keys/:keyId",
		"POST /api/v1/profiles/:profileId/api-keys/:keyId/rotate",
		"DELETE /api/v1/profiles/:profileId/api-keys/:keyId",
		"POST /api/v1/fragments",
		"GET /api/v1/fragments",
		"GET /api/v1/fragments/:id",
		"DELETE /api/v1/fragments/:id",
		"POST /api/v1/fragments/:id/retract",
		"POST /api/v1/claims",
		"GET /api/v1/claims",
		"GET /api/v1/claims/:id",
		"DELETE /api/v1/claims/:id",
		"POST /api/v1/claims/:id/verify",
		"POST /api/v1/claims/:id/promote",
		"GET /api/v1/facts",
		"GET /api/v1/facts/:id",
		"POST /api/v1/facts/:id/retract",
		"GET /api/v1/communities",
		"GET /api/v1/communities/:id",
		"POST /mcp",
		"GET /mcp",
		"GET /api/v1/tools",
		"POST /api/v1/tools/graph-query",
		"POST /api/v1/tools/keyword-search",
		"POST /api/v1/tools/semantic-search",
		"GET /api/v1/tools/:id",
		"POST /api/v1/tools/:name",
		"GET /api/v1/openapi.json",
		"GET /api/v1/recall",
	}

	for _, route := range required {
		if !routes[route] {
			t.Fatalf("route %q not registered", route)
		}
	}
}

func TestRegisterProtectedRoutesWithHandlersUsesAISafeOpenAPIFallback(t *testing.T) {
	e := echo.New()
	RegisterProtectedRoutesWithHandlers(e, ProtectedDeps{}, ProtectedHandlers{
		OpenAPIAISafe: func(c echo.Context) error { return c.NoContent(204) },
	})

	if !registeredRoutes(e)["GET /api/v1/openapi.json"] {
		t.Fatal("AI-safe OpenAPI fallback route not registered")
	}
}

func registeredRoutes(e *echo.Echo) map[string]bool {
	routes := make(map[string]bool)
	for _, route := range e.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	return routes
}
