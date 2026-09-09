package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

const MaxProviderDiagnosticBodyBytes = 16 << 20

// ProviderExchange is a bounded, operator-only record of one outbound model
// request and the response observed by the transport. It intentionally omits
// headers so credentials and cookies cannot enter diagnostics.
type ProviderExchange struct {
	Component              string
	Model                  string
	RequestBody            []byte
	ResponseBody           []byte
	ResponseBodyProjection []byte
	RequestContentType     string
	ResponseContentType    string
	StatusCode             int
	Outcome                string
	CaptureState           string
	StartedAt              time.Time
	CompletedAt            time.Time
}

// ExchangeRecorder receives provider exchanges for the current Remember
// operation. Implementations must copy the byte slices before retaining them.
type ExchangeRecorder interface {
	RecordProviderExchange(context.Context, ProviderExchange)
}

type exchangeRecorderContextKey struct{}

func WithExchangeRecorder(ctx context.Context, recorder ExchangeRecorder) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, exchangeRecorderContextKey{}, recorder)
}

func ExchangeRecorderFromContext(ctx context.Context) ExchangeRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(exchangeRecorderContextKey{}).(ExchangeRecorder)
	return recorder
}

// ProjectProviderExchangeBodies removes provider payload fields that are not
// safe for operator diagnostics. Provider prompts, generated content, input
// text, and embedding vectors are represented only by bounded shape metadata;
// status and usage fields remain available for troubleshooting.
func ProjectProviderExchangeBodies(component string, requestBody, responseBody []byte) ([]byte, []byte) {
	return projectProviderRequest(component, requestBody), projectProviderResponse(component, responseBody)
}

// ProjectEmbeddingProviderResponse keeps the safe response metadata while
// using already-decoded vector dimensions. This avoids allocating one JSON
// value per embedding element just to count dimensions.
func ProjectEmbeddingProviderResponse(body []byte, dimensions []int) []byte {
	if len(body) == 0 {
		return nil
	}
	var envelope struct {
		ID                json.RawMessage `json:"id"`
		Object            json.RawMessage `json:"object"`
		Model             json.RawMessage `json:"model"`
		SystemFingerprint json.RawMessage `json:"system_fingerprint"`
		Usage             json.RawMessage `json:"usage"`
		Error             json.RawMessage `json:"error"`
		Data              *[]struct {
			Index  json.RawMessage `json:"index"`
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return providerDiagnosticFormatMarker("non_json", len(body))
	}
	raw := make(map[string]json.RawMessage, 6)
	for key, value := range map[string]json.RawMessage{
		"id": envelope.ID, "object": envelope.Object, "model": envelope.Model,
		"system_fingerprint": envelope.SystemFingerprint, "usage": envelope.Usage, "error": envelope.Error,
	} {
		if len(value) > 0 {
			raw[key] = value
		}
	}
	projection := providerResponseMetadata(raw)
	if envelope.Data != nil {
		items := make([]map[string]any, 0, len(*envelope.Data))
		for index, item := range *envelope.Data {
			object := ""
			_ = json.Unmarshal(item.Object, &object)
			projectionItem := map[string]any{
				"object":               object,
				"embedding_dimensions": 0,
			}
			if index < len(dimensions) {
				projectionItem["embedding_dimensions"] = dimensions[index]
			}
			var itemIndex int
			if json.Unmarshal(item.Index, &itemIndex) == nil {
				projectionItem["index"] = itemIndex
			}
			items = append(items, projectionItem)
		}
		projection["data"] = items
	}
	if len(projection) == 0 {
		projection["field_count"] = 0
	}
	return marshalProviderProjection(projection)
}

func projectProviderRequest(component string, body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return providerDiagnosticFormatMarker("non_json", len(body))
	}

	projection := map[string]any{}
	if model, ok := jsonStringField(raw, "model"); ok {
		projection["model"] = model
	}
	switch component {
	case "assessor":
		var messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if value, ok := raw["messages"]; ok && json.Unmarshal(value, &messages) == nil {
			projection["message_count"] = len(messages)
			roles := make([]string, 0, len(messages))
			contentBytes := make([]int, 0, len(messages))
			for _, message := range messages {
				roles = append(roles, message.Role)
				contentBytes = append(contentBytes, jsonStringByteLength(message.Content))
			}
			projection["message_roles"] = roles
			projection["message_content_bytes"] = contentBytes
		}
		if value, ok := jsonNumberField(raw, "temperature"); ok {
			projection["temperature"] = value
		}
		if responseFormat, ok := raw["response_format"]; ok {
			var format map[string]json.RawMessage
			if json.Unmarshal(responseFormat, &format) == nil {
				if value, ok := jsonStringField(format, "type"); ok {
					projection["response_format_type"] = value
				}
				if schemaRaw, ok := format["json_schema"]; ok {
					var schema map[string]json.RawMessage
					if json.Unmarshal(schemaRaw, &schema) == nil {
						if value, ok := jsonStringField(schema, "name"); ok {
							projection["response_schema_name"] = value
						}
						if value, ok := jsonBoolField(schema, "strict"); ok {
							projection["response_schema_strict"] = value
						}
					}
				}
			}
		}
	case "embedding":
		if dimensions, ok := jsonIntField(raw, "dimensions"); ok {
			projection["dimensions"] = dimensions
		}
		if value, ok := raw["input"]; ok {
			var inputs []json.RawMessage
			if json.Unmarshal(value, &inputs) == nil {
				projection["input_count"] = len(inputs)
				inputBytes := make([]int, 0, len(inputs))
				for _, input := range inputs {
					inputBytes = append(inputBytes, jsonStringByteLength(input))
				}
				projection["input_bytes"] = inputBytes
			}
		}
	}
	if len(projection) == 0 {
		projection["field_count"] = len(raw)
	}
	return marshalProviderProjection(projection)
}

func projectProviderResponse(component string, body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return providerDiagnosticFormatMarker("non_json", len(body))
	}
	projection := providerResponseMetadata(raw)
	switch component {
	case "assessor":
		var choices []struct {
			Index        *int   `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if value, ok := raw["choices"]; ok && json.Unmarshal(value, &choices) == nil {
			items := make([]map[string]any, 0, len(choices))
			for _, choice := range choices {
				item := map[string]any{
					"finish_reason":         choice.FinishReason,
					"message_role":          choice.Message.Role,
					"message_content_bytes": jsonStringByteLength(choice.Message.Content),
				}
				if choice.Index != nil {
					item["index"] = *choice.Index
				}
				items = append(items, item)
			}
			projection["choices"] = items
		}
	case "embedding":
		var data []struct {
			Index     *int      `json:"index"`
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
		}
		if value, ok := raw["data"]; ok && json.Unmarshal(value, &data) == nil {
			items := make([]map[string]any, 0, len(data))
			for _, item := range data {
				projectionItem := map[string]any{
					"object":               item.Object,
					"embedding_dimensions": len(item.Embedding),
				}
				if item.Index != nil {
					projectionItem["index"] = *item.Index
				}
				items = append(items, projectionItem)
			}
			projection["data"] = items
		}
	}
	if len(projection) == 0 {
		projection["field_count"] = len(raw)
	}
	return marshalProviderProjection(projection)
}

func providerResponseMetadata(raw map[string]json.RawMessage) map[string]any {
	projection := map[string]any{}
	for _, field := range []string{"id", "object", "model", "system_fingerprint"} {
		if value, ok := jsonStringField(raw, field); ok {
			projection[field] = value
		}
	}
	if usageRaw, ok := raw["usage"]; ok {
		var usage map[string]json.RawMessage
		if json.Unmarshal(usageRaw, &usage) == nil {
			usageProjection := map[string]any{}
			for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens"} {
				if value, ok := jsonIntField(usage, field); ok {
					usageProjection[field] = value
				}
			}
			if len(usageProjection) > 0 {
				projection["usage"] = usageProjection
			}
		}
	}
	if errorRaw, ok := raw["error"]; ok {
		var providerError map[string]json.RawMessage
		if json.Unmarshal(errorRaw, &providerError) == nil {
			errorProjection := map[string]any{}
			for _, field := range []string{"type", "code", "param"} {
				if value, ok := jsonStringField(providerError, field); ok {
					errorProjection[field] = value
				}
			}
			if len(errorProjection) > 0 {
				projection["error"] = errorProjection
			}
		}
	}
	return projection
}

func jsonStringField(fields map[string]json.RawMessage, name string) (string, bool) {
	value, ok := fields[name]
	if !ok {
		return "", false
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", false
	}
	return result, true
}

func jsonBoolField(fields map[string]json.RawMessage, name string) (bool, bool) {
	value, ok := fields[name]
	if !ok {
		return false, false
	}
	var result bool
	if err := json.Unmarshal(value, &result); err != nil {
		return false, false
	}
	return result, true
}

func jsonNumberField(fields map[string]json.RawMessage, name string) (json.Number, bool) {
	value, ok := fields[name]
	if !ok {
		return "", false
	}
	var result json.Number
	if err := json.Unmarshal(value, &result); err != nil {
		return "", false
	}
	return result, true
}

func jsonIntField(fields map[string]json.RawMessage, name string) (int64, bool) {
	value, ok := fields[name]
	if !ok {
		return 0, false
	}
	var result int64
	if err := json.Unmarshal(value, &result); err != nil {
		return 0, false
	}
	return result, true
}

func jsonStringByteLength(value json.RawMessage) int {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return 0
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return len([]byte(text))
	}
	return len(value)
}

func providerDiagnosticFormatMarker(format string, byteCount int) []byte {
	return marshalProviderProjection(map[string]any{"format": format, "byte_count": byteCount})
}

func marshalProviderProjection(value map[string]any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"format":"projection_failed"}`)
	}
	return encoded
}
