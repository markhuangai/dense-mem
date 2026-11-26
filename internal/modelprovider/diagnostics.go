package modelprovider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const MaxProviderDiagnosticBodyBytes = 16 << 20
const MaxDecisionRequestWireBytes = 64 << 20

const maxProviderDiagnosticIdentifierBytes = 128

// ProviderExchange is a bounded, operator-only record of one outbound model
// request and the response observed by the transport. It intentionally omits
// headers so credentials and cookies cannot enter diagnostics.
type ProviderExchange struct {
	Component              string
	Model                  string
	RequestBody            []byte
	ResponseBody           []byte
	ResponseBodyProjection []byte
	ResponseBodySize       int
	RequestContentType     string
	ResponseContentType    string
	StatusCode             int
	Outcome                string
	CaptureState           string
	CaptureReason          string
	StartedAt              time.Time
	CompletedAt            time.Time
}

// ExchangeRecorder receives provider exchanges for the current Remember
// operation. Implementations must copy the byte slices before retaining them.
type ExchangeRecorder interface {
	RecordProviderExchange(context.Context, ProviderExchange)
}

type SnapshotRecorder interface {
	ExchangeRecorder
	Snapshot() []ProviderExchange
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

func providerResponseMetadata(raw map[string]json.RawMessage) map[string]any {
	projection := map[string]any{}
	for _, field := range []string{"id", "object", "model", "system_fingerprint"} {
		if value, ok := jsonDiagnosticIdentifierField(raw, field); ok {
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
				if value, ok := jsonDiagnosticIdentifierField(providerError, field); ok {
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

func jsonDiagnosticIdentifierField(fields map[string]json.RawMessage, name string) (string, bool) {
	value, ok := jsonStringField(fields, name)
	if !ok || len(value) == 0 || len(value) > maxProviderDiagnosticIdentifierBytes {
		return "", false
	}
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("_-.:/[]", rune(char)) {
			continue
		}
		return "", false
	}
	return value, true
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
