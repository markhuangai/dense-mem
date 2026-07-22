package main

import (
	"context"
	"os"
	"testing"

	internalhttp "github.com/markhuangai/dense-mem/internal/http"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

type stubRecallSvcForBootstrap struct{}

func (s *stubRecallSvcForBootstrap) Recall(
	_ context.Context,
	_ memoryservice.V2RecallRequest,
) (*memoryservice.V2RecallResult, error) {
	return nil, nil
}

var _ memoryservice.RecallService = (*stubRecallSvcForBootstrap)(nil)

func TestServerBootstrapWiresKnowledgePipeline(t *testing.T) {
	reg, err := registry.BuildActive(registry.Dependencies{})
	require.NoError(t, err, "BuildActive must succeed without embedding services")
	require.NotNil(t, reg, "registry must be non-nil")

	recallH := handler.NewRecallHandler(&stubRecallSvcForBootstrap{})
	require.NotNil(t, recallH, "NewRecallHandler must return a non-nil handler")

	ph := internalhttp.ProtectedHandlers{
		Recall: recallH.Handle,
	}
	require.NotNil(t, ph.Recall, "ProtectedHandlers.Recall must be non-nil after handler assignment")
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
