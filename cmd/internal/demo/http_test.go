package demo

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestRegisterRoutesUsesUserPortalAPIPrefix(t *testing.T) {
	e := echo.New()
	RegisterRoutes(e, nil, "")

	paths := map[string]struct{}{}
	for _, route := range e.Routes() {
		if route.Method == http.MethodPost {
			paths[route.Path] = struct{}{}
		}
	}
	require.Contains(t, paths, "/ui/api/demo/session")
	require.NotContains(t, paths, "/demo/api/session")
}
