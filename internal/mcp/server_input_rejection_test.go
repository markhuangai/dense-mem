package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestServerClassifiesContractTenantOverrideRejection(t *testing.T) {
	logger, logs := testLogger(t)
	reg := registry.New()
	tool := registry.ContractTools()[0]
	tool.Visibility = "active"
	tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
		t.Fatal("contract tenant override invoked remember")
		return nil, nil
	}
	require.NoError(t, reg.Register(tool))
	teamID := uuid.New()
	server := NewServer(reg, teamID.String(), logger)

	_, rpcErr := server.invokeTool(context.Background(), registry.ToolRemember, map[string]any{
		"team_id": uuid.NewString(),
	})

	require.NotNil(t, rpcErr)
	require.Equal(t, errCodeInvalidParams, rpcErr.Code)
	require.Contains(t, logs.String(), `"reason_code":"tenant_override_rejected"`)
	require.NotContains(t, logs.String(), `"reason_code":"contract_validation_failed"`)
}
