package modelprovider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStructuredRequestSupportsDisabledTemperatureAndRejectsInvalidSchema(t *testing.T) {
	body, err := MarshalStructuredRequest(StructuredRequest{Model: "model", SchemaName: "schema", Schema: map[string]any{"type": "object"}})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	_, hasTemperature := decoded["temperature"]
	require.False(t, hasTemperature)

	_, err = MarshalStructuredRequest(StructuredRequest{Model: "model", SchemaName: "schema", Schema: map[string]any{"unsupported": func() {}}})
	require.Error(t, err)
	_, err = CountStructuredRequestTokens(StructuredRequest{Model: "model", SchemaName: "schema", Schema: map[string]any{"type": "object"}}, "not-a-tokenizer")
	require.Error(t, err)
}

func TestStructuredRequestMatchesTokenMeasurement(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
	}
	messages := []Message{
		{Role: "system", Content: "system instruction"},
		{Role: "user", Content: `{"request_id":"request-1"}`},
	}
	request := StructuredRequest{Model: "assessor-model", SchemaName: "assessment", Schema: schema, Temperature: StructuredTemperature(false), Messages: messages}
	body, err := MarshalStructuredRequest(request)
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
	measured, err := CountStructuredRequestTokens(request, "o200k_base")
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
