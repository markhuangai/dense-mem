package main

import (
	"context"
	"os"
	"testing"

	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

// stubRecallSvcForBootstrap implements recallservice.RecallService for
// wiring smoke tests only. Mocks are kept in *_test.go per
// memory/feedback_mock_placement.md — cross-pkg consumers use a local stub.
type stubRecallSvcForBootstrap struct{}

func (s *stubRecallSvcForBootstrap) Recall(
	_ context.Context,
	_ string,
	_ recallservice.RecallRequest,
) ([]recallservice.RecallHit, error) {
	return nil, nil
}

var _ recallservice.RecallService = (*stubRecallSvcForBootstrap)(nil)

// TestServerBootstrapWiresKnowledgePipeline is a compile-and-wire smoke test
// that verifies the server bootstrap can construct all knowledge-pipeline
// handlers and assign them to ProtectedHandlers.
//
// Specifically it checks:
//   - registry.BuildDefault succeeds without embedding services for isolated
//     wiring tests and the MCP HTTP path
//   - handler.NewRecallHandler can be constructed and its Handle method
//     assigned to ProtectedHandlers.Recall (AC-55, AC-62)
//
// No HTTP requests are issued and no database connections are opened.
func TestServerBootstrapWiresKnowledgePipeline(t *testing.T) {
	// registry.BuildDefault must succeed when no embedding services are wired.
	// This mirrors isolated wiring tests that do not boot the full production server.
	reg, err := registry.BuildDefault(registry.Dependencies{})
	require.NoError(t, err, "BuildDefault must succeed without embedding services")
	require.NotNil(t, reg, "registry must be non-nil")

	// RecallHandler must be constructable from any RecallService implementation.
	// In production, recallSvc is nil when embedding is unconfigured; the
	// handler is only wired when recallSvc != nil (matching fragmentCreateHandler).
	recallH := handler.NewRecallHandler(&stubRecallSvcForBootstrap{})
	require.NotNil(t, recallH, "NewRecallHandler must return a non-nil handler")

	// Verify that ProtectedHandlers.Recall accepts the assignment.
	// This is a compile-time + runtime check that the field exists and has the
	// right type.
	ph := internalhttp.ProtectedHandlers{
		Recall: recallH.Handle,
	}
	require.NotNil(t, ph.Recall, "ProtectedHandlers.Recall must be non-nil after handler assignment")
}

func TestRecallRegistryUsesSemanticRecallService(t *testing.T) {
	body, err := os.ReadFile("main.go")
	require.NoError(t, err)
	source := string(body)

	require.Contains(t, source, "recallRegistrySvc := recallservice.NewSemanticRecallServiceWithRanking(semanticRepo, semanticRecallRanking, retryEmbedder)")
	require.NotContains(t, source, "tieredRecallSvc := recallservice.NewRecallServiceWithTiers(")
	require.NotContains(t, source, "recallRegistrySvc = recallservice.NewRecallService(")
}

func TestServerMainWiresProtectedKnowledgeHandlers(t *testing.T) {
	body, err := os.ReadFile("main.go")
	require.NoError(t, err)
	source := string(body)

	require.Contains(t, source, "toolCatalogHandler := handler.NewToolCatalogHandlerWithRuntimeConfig(toolRegistry, appConfigService)")
	require.Contains(t, source, "toolReadHandler := handler.NewToolReadHandlerWithRuntimeConfig(toolRegistry, appConfigService)")
	require.Contains(t, source, "toolExecuteHandler := handler.NewToolExecuteHandlerWithRuntimeConfig(toolRegistry, appConfigService)")
	require.Contains(t, source, "openAPIGen := openapi.New(toolRegistry, openapi.DefaultRoutes())")
	require.Contains(t, source, "recallHandler := handler.NewRecallHandler(recallRegistrySvc)")
	require.Contains(t, source, "dreamHandler := handler.NewDreamHandler(dreamSvc)")

	require.Contains(t, source, "ToolCatalog:    toolCatalogHandler.Handle")
	require.Contains(t, source, "GetTool:        toolReadHandler.Handle")
	require.Contains(t, source, "ExecuteTool:    toolExecuteHandler.Handle")
	require.Contains(t, source, "OpenAPIAISafe:  openAPIAISafeHandler.Handle")
	require.Contains(t, source, "OpenAPIFull:    openAPIFullHandler.Handle")
	require.Contains(t, source, "Recall:         recallHandler.Handle")
	require.Contains(t, source, "DreamingStatus: dreamHandler.Status")
	require.Contains(t, source, "DreamingRuns:   dreamHandler.Runs")
	require.Contains(t, source, "DreamList:      dreamHandler.List")
	require.Contains(t, source, "DreamGet:       dreamHandler.Get")
}
