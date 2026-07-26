package main

import (
	"os"
	"testing"

	"github.com/labstack/echo/v4"
	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

func TestServerBootstrapWiresKnowledgePipeline(t *testing.T) {
	reg, err := registry.BuildActive(registry.Dependencies{})
	require.NoError(t, err, "BuildActive must succeed without embedding services")
	require.NotNil(t, reg, "registry must be non-nil")

	ph := internalhttp.ProtectedHandlers{
		MCPPost: func(c echo.Context) error { return nil },
		MCPGet:  func(c echo.Context) error { return nil },
	}
	require.NotNil(t, ph.MCPPost, "ProtectedHandlers.MCPPost must be non-nil after handler assignment")
	require.NotNil(t, ph.MCPGet, "ProtectedHandlers.MCPGet must be non-nil after handler assignment")
}

func TestServerBootstrapUsesActiveRegistry(t *testing.T) {
	body, err := os.ReadFile("../internal/serverapp/server.go")
	require.NoError(t, err)
	source := string(body)

	require.Contains(t, source, "registry.BuildActive(")
	require.NotContains(t, source, "registry.BuildDefault(")

	body, err = os.ReadFile("main.go")
	require.NoError(t, err)
	require.Contains(t, string(body), "serverapp.RunActiveServer(")
}
