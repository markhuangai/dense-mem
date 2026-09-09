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

func TestProjectEmbeddingProviderResponseUsesDecodedDimensions(t *testing.T) {
	response := ProjectEmbeddingProviderResponse(
		[]byte(`{"model":"embedding-model","data":[{"index":4,"object":"embedding","embedding":[0.1,0.2]}]}`),
		[]int{2},
	)
	require.Equal(t, `{"data":[{"embedding_dimensions":2,"index":4,"object":"embedding"}],"model":"embedding-model"}`, string(response))
}

func TestProjectProviderExchangeBodiesMarksMalformedPayloadWithoutRetainingIt(t *testing.T) {
	request, response := ProjectProviderExchangeBodies("assessor", []byte("private prompt"), []byte("private provider response"))
	require.Equal(t, `{"byte_count":14,"format":"non_json"}`, string(request))
	require.Equal(t, `{"byte_count":25,"format":"non_json"}`, string(response))
}

func TestProjectProviderExchangeBodiesRetainsOnlySafeResponseMetadata(t *testing.T) {
	request, response := ProjectProviderExchangeBodies("other", []byte(`{"model":"model","unexpected":true}`), []byte(`{"id":"id","object":"response","model":"model","system_fingerprint":"fingerprint","usage":{"input_tokens":2,"output_tokens":3},"error":{"type":"bad_request","code":"invalid","param":"model","message":"private"}}`))
	require.Equal(t, `{"model":"model"}`, string(request))
	require.Contains(t, string(response), `"system_fingerprint":"fingerprint"`)
	require.Contains(t, string(response), `"input_tokens":2`)
	require.Contains(t, string(response), `"output_tokens":3`)
	require.NotContains(t, string(response), "private")
	require.Nil(t, mustProjectionBodies(t, "other", nil, nil))
}

func TestProjectProviderExchangeBodiesHandlesSparseAndNonStringFields(t *testing.T) {
	request, response := ProjectProviderExchangeBodies("assessor", []byte(`{"temperature":0.5,"messages":[{"role":"user","content":null},{"role":"assistant","content":{"parts":[1]}}],"response_format":{"type":"json_schema","json_schema":{"strict":false}}}`), []byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":null}}]}`))
	require.Contains(t, string(request), `"temperature":0.5`)
	require.Contains(t, string(request), `"message_content_bytes":[0,13]`)
	require.Contains(t, string(request), `"response_schema_strict":false`)
	require.Contains(t, string(response), `"message_content_bytes":0`)
	require.Contains(t, string(response), `"finish_reason":"length"`)

	emptyRequest, emptyResponse := ProjectProviderExchangeBodies("other", []byte(`{}`), []byte(`{"error":{"message":"private"}}`))
	require.Equal(t, `{"field_count":0}`, string(emptyRequest))
	require.Equal(t, `{"field_count":1}`, string(emptyResponse))
	request, response = ProjectProviderExchangeBodies("other", nil, nil)
	require.Nil(t, request)
	require.Nil(t, response)
}

func mustProjectionBodies(t *testing.T, component string, request, response []byte) []byte {
	t.Helper()
	projectedRequest, projectedResponse := ProjectProviderExchangeBodies(component, request, response)
	require.Nil(t, projectedRequest)
	return projectedResponse
}
