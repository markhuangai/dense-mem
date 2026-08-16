package mcp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestMCP_InitializeNegotiatesCompatibleProtocolVersions(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "pA", logger)

	for _, requested := range []string{
		"2024-11-05",
		"2025-03-26",
		"2025-06-18",
		ProtocolVersion,
	} {
		t.Run(requested, func(t *testing.T) {
			out := runRPC(t, server, fmt.Sprintf(
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q}}`,
				requested,
			))
			var response rpcResp
			require.NoError(t, json.Unmarshal([]byte(out), &response))
			require.Nil(t, response.Error)

			var result map[string]any
			require.NoError(t, json.Unmarshal(response.Result, &result))
			require.Equal(t, requested, result["protocolVersion"])
		})
	}
}

func TestSDKInitializeFallsBackToLatestProtocolVersion(t *testing.T) {
	logger, _ := testLogger(t)
	server := NewServer(registry.New(), "pA", logger)

	out := runRPC(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
	var response rpcResp
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Nil(t, response.Error)

	var result map[string]any
	require.NoError(t, json.Unmarshal(response.Result, &result))
	require.Equal(t, ProtocolVersion, result["protocolVersion"])
}
