package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

func TestSDKContractToolFailureIsStructuredActionableError(t *testing.T) {
	tool := registry.ContractTools()[0]
	tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
		return nil, errors.New("provider secret details")
	}
	reg := registry.New()
	require.NoError(t, reg.Register(tool))
	server := NewServer(reg, "profile-a", nil)

	result, err := server.sdkToolHandler(tool.Name)(context.Background(), &sdkmcp.CallToolRequest{
		Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"idempotency_key":"key","evidence":[{"content":"hello"}]}`)},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.NotNil(t, result.StructuredContent)
	text := result.Content[0].(*sdkmcp.TextContent).Text
	encoded, marshalErr := json.Marshal(result.StructuredContent)
	require.NoError(t, marshalErr)
	require.JSONEq(t, string(encoded), text)
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &data))
	require.Equal(t, "provider_unavailable", data["code"])
	require.NotContains(t, text, "provider secret details")
	require.NotEmpty(t, data["reason_code"])
	require.NotEmpty(t, data["remediation"])
	require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: tool.OutputSchema}, data))
}

func TestSDKActionableErrorsPreserveCapabilityRecoveryAndOutputSchema(t *testing.T) {
	for _, test := range []struct {
		name         string
		toolName     string
		arguments    string
		wantRemediat string
	}{
		{
			name:         "read timeout",
			toolName:     registry.ToolRecallMemory,
			arguments:    `{"query":"hello"}`,
			wantRemediat: "Retry the read request with the same arguments after the timeout clears.",
		},
		{
			name:         "mutation timeout",
			toolName:     registry.ToolRemember,
			arguments:    `{"idempotency_key":"key","evidence":[{"content":"hello"}]}`,
			wantRemediat: "Retry the same request with the same idempotency key after the timeout clears.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := contractToolByName(t, test.toolName)
			tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
				return nil, context.DeadlineExceeded
			}
			reg := registry.New()
			require.NoError(t, reg.Register(tool))
			server := NewServer(reg, "profile-a", nil)
			result, err := server.sdkToolHandler(tool.Name)(context.Background(), &sdkmcp.CallToolRequest{
				Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage(test.arguments)},
			})
			require.NoError(t, err)
			require.True(t, result.IsError)
			data := sdkStructuredData(t, result)
			require.Equal(t, test.wantRemediat, data["remediation"])
			require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: tool.OutputSchema}, data))
		})
	}
}

func TestSDKUnavailableAndSerializationFailuresAreActionable(t *testing.T) {
	for _, test := range []struct {
		name   string
		invoke func(registry.Tool) registry.Tool
		want   string
	}{
		{
			name: "unavailable invoker",
			invoke: func(tool registry.Tool) registry.Tool {
				return tool
			},
			want: "tool_unavailable",
		},
		{
			name: "serialization fallback",
			invoke: func(tool registry.Tool) registry.Tool {
				tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
					return map[string]any{"unencodable": func() {}}, nil
				}
				return tool
			},
			want: "tool_result_serialization_failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := test.invoke(contractToolByName(t, registry.ToolRecallMemory))
			reg := registry.New()
			require.NoError(t, reg.Register(tool))
			server := NewServer(reg, "profile-a", nil)
			result, err := server.sdkToolHandler(tool.Name)(context.Background(), &sdkmcp.CallToolRequest{
				Params: &sdkmcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"query":"hello"}`)},
			})
			require.NoError(t, err)
			require.True(t, result.IsError)
			data := sdkStructuredData(t, result)
			require.Equal(t, test.want, data["reason_code"])
			require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: tool.OutputSchema}, data))
		})
	}
}

func TestSDKEvaluationToolFailureIsStructuredActionableError(t *testing.T) {
	reg := registry.New()
	require.NoError(t, reg.Register(registry.Tool{
		Name:        "eval_run_recall_case",
		InputSchema: map[string]any{"type": "object"},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return nil, errors.New("evaluation backend unavailable")
		},
	}))
	server := NewServer(reg, "profile-a", nil)
	result, err := server.sdkToolHandler("eval_run_recall_case")(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, result.IsError)
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*sdkmcp.TextContent).Text), &data))
	require.Equal(t, "provider_unavailable", data["code"])
}

func contractToolByName(t *testing.T, name string) registry.Tool {
	t.Helper()
	for _, tool := range registry.ContractTools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("contract tool %q not found", name)
	return registry.Tool{}
}

func sdkStructuredData(t *testing.T, result *sdkmcp.CallToolResult) map[string]any {
	t.Helper()
	require.NotNil(t, result)
	require.NotNil(t, result.StructuredContent)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var data map[string]any
	require.NoError(t, json.Unmarshal(encoded, &data))
	text := result.Content[0].(*sdkmcp.TextContent).Text
	require.JSONEq(t, string(encoded), text)
	return data
}
