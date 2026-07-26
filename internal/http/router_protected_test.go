package http

import (
	"context"
	"sync"
	"testing"
	"time"

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

func (routerAPIKeyService) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}

func (routerAPIKeyService) UpdateScopesForProfile(context.Context, uuid.UUID, uuid.UUID, []string, *string, string, string, string) (*domain.APIKey, error) {
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

func (routerAPIKeyService) GetSSOOwnedKey(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (routerAPIKeyService) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}

type routerAPIKeyRepo struct {
	key *domain.APIKey

	mu           sync.Mutex
	touchedID    uuid.UUID
	touchedCount int
}

func (r *routerAPIKeyRepo) CreateStandardKey(context.Context, *domain.APIKey) error {
	return nil
}

func (r *routerAPIKeyRepo) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	return nil, nil
}

func (r *routerAPIKeyRepo) CountByProfile(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (r *routerAPIKeyRepo) GetSSOOwnedKey(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (r *routerAPIKeyRepo) GetActiveByPrefix(_ context.Context, prefix string) (*domain.APIKey, error) {
	if r.key != nil && r.key.KeyPrefix == prefix {
		return r.key, nil
	}
	return nil, nil
}

func (r *routerAPIKeyRepo) RevokeForProfile(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string, []string) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) UpdateScopesForProfile(context.Context, uuid.UUID, uuid.UUID, []string) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, string, string, string, *time.Time) (int64, error) {
	return 0, nil
}

func (r *routerAPIKeyRepo) TouchLastUsed(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchedID = id
	r.touchedCount++
	return nil
}

func (r *routerAPIKeyRepo) touched(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.touchedID == id && r.touchedCount > 0
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
		MCPPost: noContent,
		MCPGet:  noContent,
	})

	routes := registeredRoutes(e)
	required := []string{
		"POST /mcp",
		"GET /mcp",
	}

	for _, route := range required {
		if !routes[route] {
			t.Fatalf("route %q not registered", route)
		}
	}

	removed := []string{
		"GET /ui/api/team",
		"PATCH /ui/api/team",
		"GET /ui/api/team/audit-log",
		"GET /ui/api/team/profiles",
		"POST /ui/api/team/profiles",
		"GET /ui/api/team/profiles/:profileId",
		"GET /ui/api/team/profiles/:profileId/api-keys",
		"GET /mcp/tools",
		"GET /mcp/tools/:id",
		"POST /mcp/tools/:name",
		"GET /ui/api/recall",
		"GET /ui/api/dreaming/status",
		"GET /ui/api/dreams",
		"POST /ui/api/team/query/stream",
		"POST /ui/api/evidence",
		"GET /ui/api/evidence",
		"GET /ui/api/evidence/:id",
		"DELETE /ui/api/evidence/:id",
		"POST /ui/api/evidence/:id/retract",
		"POST /ui/api/relationships",
		"GET /ui/api/relationships",
		"GET /ui/api/relationships/:id",
		"DELETE /ui/api/relationships/:id",
		"POST /ui/api/relationships/:id/verify",
		"POST /ui/api/relationships/:id/promote",
		"GET /ui/api/relationships",
		"GET /ui/api/relationships/:id",
		"POST /ui/api/relationships/:id/retract",
		"GET /ui/api/communities",
		"GET /ui/api/communities/:id",
		"POST /mcp/tools/graph_query",
		"POST /mcp/tools/keyword_search",
		"POST /mcp/tools/semantic_search",
	}
	for _, route := range removed {
		if routes[route] {
			t.Fatalf("removed route %q was registered", route)
		}
	}
}

func registeredRoutes(e *echo.Echo) map[string]bool {
	routes := make(map[string]bool)
	for _, route := range e.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	return routes
}
