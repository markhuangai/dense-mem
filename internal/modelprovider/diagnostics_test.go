package modelprovider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type diagnosticsTestContextKey string
type diagnosticsTestRecorder struct{}

func (diagnosticsTestRecorder) RecordProviderExchange(context.Context, ProviderExchange) {}

func TestExchangeRecorderContextRoundTrip(t *testing.T) {
	const key diagnosticsTestContextKey = "diagnostic"
	base := context.WithValue(context.Background(), key, "kept")
	recorder := diagnosticsTestRecorder{}
	wrapped := WithExchangeRecorder(base, recorder)

	require.Equal(t, "kept", wrapped.Value(key))
	require.Equal(t, recorder, ExchangeRecorderFromContext(wrapped))
	require.Equal(t, base, WithExchangeRecorder(base, nil))
	require.Nil(t, ExchangeRecorderFromContext(context.Background()))
}

func TestProjectProviderExchangeBodiesAllowListsDiagnosticMetadata(t *testing.T) {
	request, response := ProjectProviderExchangeBodies("assessor", []byte(`{
		"model":"assessor-model",
		"messages":[{"role":"system","content":"private system prompt"},{"role":"user","content":"private evidence"}],
		"response_format":{"type":"json_schema","json_schema":{"name":"assessment","strict":true,"schema":{"properties":{"secret":{"type":"string"}}}}},
		"api_key":"provider-secret"
	}`), []byte(`{
		"id":"response-1","model":"assessor-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"private provider output"}}],
		"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16},
		"error":{"type":"provider_error","code":"safe-code","message":"private provider error"}
	}`))

	var requestFields map[string]any
	var responseFields map[string]any
	require.NoError(t, json.Unmarshal(request, &requestFields))
	require.NoError(t, json.Unmarshal(response, &responseFields))
	require.Equal(t, "assessor-model", requestFields["model"])
	require.Equal(t, float64(2), requestFields["message_count"])
	require.NotContains(t, string(request), "private system prompt")
	require.NotContains(t, string(request), "private evidence")
	require.NotContains(t, string(request), "provider-secret")
	require.NotContains(t, string(request), "properties")
	require.NotContains(t, string(response), "private provider output")
	require.NotContains(t, string(response), "private provider error")
	require.NotContains(t, string(response), `"content":`)
	require.Equal(t, "safe-code", responseFields["error"].(map[string]any)["code"])
}

func TestProjectProviderExchangeBodiesOmitsEmbeddingInputsAndVectors(t *testing.T) {
	request, response := ProjectProviderExchangeBodies("embedding", []byte(`{"model":"embedding-model","input":["private evidence","more evidence"],"dimensions":3}`), []byte(`{"model":"embedding-model","data":[{"index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`))

	require.NotContains(t, string(request), "private evidence")
	require.NotContains(t, string(response), "0.1")
	require.Contains(t, string(request), `"input_count":2`)
	require.Contains(t, string(response), `"embedding_dimensions":3`)
}

func TestProjectProviderExchangeBodiesMarksMalformedPayloadWithoutRetainingIt(t *testing.T) {
	request, response := ProjectProviderExchangeBodies("assessor", []byte("private prompt"), []byte("private provider response"))
	require.Equal(t, `{"byte_count":14,"format":"non_json"}`, string(request))
	require.Equal(t, `{"byte_count":25,"format":"non_json"}`, string(response))
}
