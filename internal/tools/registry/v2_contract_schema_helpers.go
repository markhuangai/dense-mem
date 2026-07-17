package registry

import "github.com/markhuangai/dense-mem/internal/domain"

const (
	v2MemoryPackArtifactMaxLength = 4 * 1024 * 1024
	v2MetadataMaxProperties       = 50
)

func v2ClosedObject(required []string, properties map[string]any) map[string]any {
	v2RequireNonEmptyStrings(required, properties)
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func v2NonEmptyString(description string, maxLength int) map[string]any {
	schema := schemaString(description, maxLength)
	schema["minLength"] = 1
	return schema
}

func v2RequireNonEmptyStrings(required []string, properties map[string]any) {
	for _, field := range required {
		property, ok := properties[field].(map[string]any)
		if !ok || property["type"] != "string" {
			continue
		}
		if _, exists := property["minLength"]; !exists {
			property["minLength"] = 1
		}
	}
}

func v2Array(items map[string]any, minItems int, maxItems int) map[string]any {
	schema := map[string]any{"type": "array", "items": items}
	if minItems > 0 {
		schema["minItems"] = minItems
	}
	if maxItems > 0 {
		schema["maxItems"] = maxItems
	}
	return schema
}

func v2NullableString(description string, maxLength int) map[string]any {
	schema := map[string]any{
		"type":        []any{"string", "null"},
		"description": description,
	}
	if maxLength > 0 {
		schema["maxLength"] = maxLength
	}
	return schema
}

func v2NullableDateTime(description string) map[string]any {
	return map[string]any{
		"type":             []any{"string", "null"},
		"format":           "date-time",
		"x-enforce-format": true,
		"description":      description,
	}
}

func v2NullableNumber(description string, minimum float64, maximum float64) map[string]any {
	return map[string]any{
		"type":        []any{"number", "null"},
		"minimum":     minimum,
		"maximum":     maximum,
		"description": description,
	}
}

func v2BoundedMap(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"maxProperties":        v2MetadataMaxProperties,
		"additionalProperties": true,
		"x-max-depth":          4,
		"x-max-bytes":          16384,
	}
}

func V2PublicErrorSchema() map[string]any {
	return v2ClosedObject(
		[]string{"code", "message", "retryable", "correlation_id"},
		map[string]any{
			"code":                schemaEnum(v2PublicErrorCodes()),
			"message":             schemaString("Bounded public error message.", 512),
			"retryable":           map[string]any{"type": "boolean"},
			"retry_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 86400},
			"correlation_id":      schemaString("Request correlation ID.", 128),
			"details":             v2BoundedMap("Code-specific bounded safe metadata."),
		},
	)
}

func v2PublicErrorCodes() []string {
	return []string{
		string(domain.V2ErrorInvalidContractVersion),
		string(domain.V2ErrorInvalidInput),
		string(domain.V2ErrorUnauthorizedScope),
		string(domain.V2ErrorWrongOwner),
		string(domain.V2ErrorConflict),
		string(domain.V2ErrorProviderUnavailable),
		string(domain.V2ErrorProviderMalformed),
		string(domain.V2ErrorDegraded),
	}
}

func v2PlacementErrorArraySchema() map[string]any {
	return v2Array(v2ClosedObject(
		[]string{"code", "message", "retryable"},
		map[string]any{
			"code":        schemaString("Typed placement error code.", 128),
			"message":     schemaString("Bounded safe placement error.", 512),
			"retryable":   map[string]any{"type": "boolean"},
			"review_task": v2NullableString("Review task handle when client action is possible.", 128),
		},
	), 0, 50)
}
