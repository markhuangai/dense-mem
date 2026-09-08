package registry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolCallerResponseBuildsStructuredAndTextEnvelope(t *testing.T) {
	result := map[string]any{"code": "provider_unavailable"}
	envelope := ToolCallerResponse(result, true)

	require.Equal(t, result, envelope["structuredContent"])
	require.Equal(t, true, envelope["isError"])
	content, ok := envelope["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	require.Equal(t, map[string]any{
		"type": "text",
		"text": `{"code":"provider_unavailable"}`,
	}, content[0])
}

func TestToolCallerResponseFallsBackWhenResultCannotBeEncoded(t *testing.T) {
	result := map[string]any{"unsupported": func() {}}
	envelope := ToolCallerResponse(result, false)

	require.Equal(t, []any{}, envelope["content"])
	require.Equal(t, result, envelope["structuredContent"])
	require.Equal(t, false, envelope["isError"])
}
