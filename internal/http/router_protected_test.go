package http

import (
	"testing"

	"github.com/labstack/echo/v5"
)

func TestRegisterProtectedRoutesWithHandlersRegistersConfiguredSurface(t *testing.T) {
	e := echo.New()
	noContent := func(c *echo.Context) error { return c.NoContent(204) }

	RegisterProtectedRoutesWithHandlers(e, ProtectedDeps{}, ProtectedHandlers{
		MCPPost: noContent,
		MCPGet:  noContent,
	})

	routes := registeredRoutes(e)
	required := []string{
		"POST /mcp",
		"GET /mcp",
		"POST /teams/:teamId/mcp",
		"GET /teams/:teamId/mcp",
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
	for _, route := range e.Router().Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	return routes
}
