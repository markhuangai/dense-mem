package assessor

import "encoding/json"

// SemanticAssessmentProviderMessage is the canonical chat message shape used
// by the structured assessor transport. Keeping this shape here lets preflight
// and the provider count and serialize the same request envelope.
type SemanticAssessmentProviderMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type semanticAssessmentProviderJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type semanticAssessmentProviderResponseFormat struct {
	Type       string                               `json:"type"`
	JSONSchema semanticAssessmentProviderJSONSchema `json:"json_schema"`
}

type semanticAssessmentProviderRequest struct {
	Model          string                                   `json:"model"`
	Messages       []SemanticAssessmentProviderMessage      `json:"messages"`
	Temperature    *float64                                 `json:"temperature,omitempty"`
	ResponseFormat semanticAssessmentProviderResponseFormat `json:"response_format"`
}

// MarshalSemanticAssessmentProviderRequest returns the exact structured
// request body used by the OpenAI-compatible assessor transport.
func MarshalSemanticAssessmentProviderRequest(
	model string,
	schemaName string,
	schema map[string]any,
	temperatureDisabled bool,
	messages []SemanticAssessmentProviderMessage,
) ([]byte, error) {
	schemaRaw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var temperature *float64
	if !temperatureDisabled {
		value := 0.0
		temperature = &value
	}
	return json.Marshal(semanticAssessmentProviderRequest{
		Model:       model,
		Messages:    append([]SemanticAssessmentProviderMessage(nil), messages...),
		Temperature: temperature,
		ResponseFormat: semanticAssessmentProviderResponseFormat{
			Type: "json_schema",
			JSONSchema: semanticAssessmentProviderJSONSchema{
				Name:   schemaName,
				Strict: true,
				Schema: schemaRaw,
			},
		},
	})
}

// CountSemanticAssessmentProviderRequestTokens counts the serialized provider
// envelope, including model, message wrappers, temperature, and response
// schema. The provider uses the same marshaler before sending the request.
func CountSemanticAssessmentProviderRequestTokens(
	model string,
	schemaName string,
	schema map[string]any,
	temperatureDisabled bool,
	messages []SemanticAssessmentProviderMessage,
	tokenizerName string,
) (int, error) {
	body, err := MarshalSemanticAssessmentProviderRequest(model, schemaName, schema, temperatureDisabled, messages)
	if err != nil {
		return 0, err
	}
	return CountTokens(string(body), tokenizerName)
}
