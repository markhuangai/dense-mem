package assessor

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarshalSemanticAssessmentProviderRequestSupportsDisabledTemperatureAndRejectsInvalidSchema(t *testing.T) {
	body, err := MarshalSemanticAssessmentProviderRequest("model", "schema", map[string]any{"type": "object"}, true, nil)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	_, hasTemperature := decoded["temperature"]
	require.False(t, hasTemperature)

	_, err = MarshalSemanticAssessmentProviderRequest("model", "schema", map[string]any{"unsupported": func() {}}, false, nil)
	require.Error(t, err)
	_, err = CountSemanticAssessmentProviderRequestTokens("model", "schema", map[string]any{"type": "object"}, false, nil, "not-a-tokenizer")
	require.Error(t, err)
}

func TestMarshalSemanticAssessmentProviderRequestMatchesTokenMeasurement(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
	}
	messages := []SemanticAssessmentProviderMessage{
		{Role: "system", Content: "system instruction"},
		{Role: "user", Content: `{"request_id":"request-1"}`},
	}
	body, err := MarshalSemanticAssessmentProviderRequest("assessor-model", "assessment", schema, false, messages)
	if err != nil {
		t.Fatalf("marshal provider request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode provider request: %v", err)
	}
	if decoded["model"] != "assessor-model" {
		t.Fatalf("model = %#v", decoded["model"])
	}
	if decoded["temperature"] != float64(0) {
		t.Fatalf("temperature = %#v", decoded["temperature"])
	}
	measured, err := CountSemanticAssessmentProviderRequestTokens("assessor-model", "assessment", schema, false, messages, "o200k_base")
	if err != nil {
		t.Fatalf("count provider request: %v", err)
	}
	expected, err := CountTokens(string(body), "o200k_base")
	if err != nil {
		t.Fatalf("count marshaled body: %v", err)
	}
	if measured != expected {
		t.Fatalf("measured tokens = %d, body tokens = %d", measured, expected)
	}
}
