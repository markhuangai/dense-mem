package mcp

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestConformanceHarnessComparesLegacyAndSDKRegistryFlows(t *testing.T) {
	logger, _ := testLogger(t)
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{
		Name:           "conformance_read",
		Description:    "read",
		InputSchema:    map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
		RequiredScopes: []string{"read"},
		Invoke: func(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
			return map[string]any{"value": input["value"]}, nil
		},
	}))
	server := NewServerWithScopes(reg, "profile-conformance", []string{"read"}, logger)
	legacy := NewLegacyHTTPHandler(server)
	sdk := server.NewSDKHTTPHandler(true)
	headers := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json, text/event-stream"}, "MCP-Protocol-Version": []string{"2025-11-25"}}
	for _, payload := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"conformance_read","arguments":{"value":"ok"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing","arguments":{}}}`,
	} {
		matched, err := ConformanceCall(context.Background(), legacy, sdk, []byte(payload), headers)
		require.NoError(t, err)
		require.True(t, matched, "legacy and SDK responses differ for %s", payload)
	}
}

func TestConformanceHarnessSupportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "profile-conformance", logger)
	legacy := NewLegacyHTTPHandler(server)
	_, err := ConformanceCall(ctx, legacy, server.NewSDKHTTPHandler(true), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`), http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json, text/event-stream"}, "MCP-Protocol-Version": []string{"2025-11-25"}})
	require.Error(t, err)
}
