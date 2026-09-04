package assessor

func closedObject(required []string, properties map[string]any) map[string]any {
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

func stringSchema(minLength int, maxLength int) map[string]any {
	schema := map[string]any{"type": "string"}
	if minLength > 0 {
		schema["minLength"] = minLength
	}
	if maxLength > 0 {
		schema["maxLength"] = maxLength
	}
	return schema
}

func nullableStringSchema(maxLength int) map[string]any {
	return map[string]any{"type": []any{"string", "null"}, "maxLength": maxLength}
}

func nullableDateTimeSchema() map[string]any {
	return map[string]any{"type": []any{"string", "null"}, "format": "date-time"}
}

func numberSchema(minimum float64, maximum float64) map[string]any {
	return map[string]any{"type": "number", "minimum": minimum, "maximum": maximum}
}

func enumSchema(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
