package main

import (
	"os"
	"strings"
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

func TestReleaseImageDoesNotUseTheE2EEntrypoint(t *testing.T) {
	body, err := os.ReadFile("../../Dockerfile")
	require.NoError(t, err)
	source := string(body)
	productionBaseStart := strings.Index(source, "FROM runtime-base AS production-base")
	require.GreaterOrEqual(t, productionBaseStart, 0)
	productionBaseEndRelative := strings.Index(source[productionBaseStart:], "FROM production-base AS preview")
	require.GreaterOrEqual(t, productionBaseEndRelative, 0)
	productionBase := source[productionBaseStart : productionBaseStart+productionBaseEndRelative]
	require.Contains(t, productionBase, "COPY --from=production-builder --chown=densemem:densemem /out/server /app/server")
	require.NotContains(t, productionBase, "e2e-builder")
	require.Contains(t, source, "FROM production-base AS production")
	require.Contains(t, source, "go build -trimpath -ldflags=\"-s -w\" -o /out/server ./cmd/e2e-server")

	serverMain, err := os.ReadFile("main.go")
	require.NoError(t, err)
	require.NotContains(t, string(serverMain), "e2eapp")
	require.NotContains(t, string(serverMain), "DENSE_MEM_E2E_WRITE_SLICE")
}
