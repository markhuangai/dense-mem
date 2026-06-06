package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/crypto"
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
		"POST /api/v1/tools/graph_query",
		"POST /api/v1/tools/keyword_search",
		"POST /api/v1/tools/semantic_search",
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

func TestRegisterProtectedRoutesWithHandlersTouchesLastUsedOnOpenAPI(t *testing.T) {
	rawKey, err := crypto.GenerateRawKey()
	if err != nil {
		t.Fatalf("GenerateRawKey: %v", err)
	}
	keyHash, err := crypto.HashKey(rawKey)
	if err != nil {
		t.Fatalf("HashKey: %v", err)
	}

	keyID := uuid.New()
	repo := &routerAPIKeyRepo{key: &domain.APIKey{
		ID:        keyID,
		TeamID:    uuid.New(),
		Name:      "docs-reader",
		KeyHash:   keyHash,
		KeyPrefix: crypto.GetKeyPrefix(rawKey),
		KeySuffix: crypto.GetKeySuffix(rawKey),
		Scopes:    []string{"read"},
		RateLimit: 100,
		CreatedAt: time.Now().UTC(),
	}}

	e := echo.New()
	RegisterProtectedRoutesWithHandlers(e, ProtectedDeps{APIKeyRepo: repo}, ProtectedHandlers{
		OpenAPIFull: func(c echo.Context) error { return c.NoContent(nethttp.StatusNoContent) },
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/openapi.json", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusNoContent, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if repo.touched(keyID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OpenAPI route did not touch last-used timestamp")
}

func registeredRoutes(e *echo.Echo) map[string]bool {
	routes := make(map[string]bool)
	for _, route := range e.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	return routes
}
