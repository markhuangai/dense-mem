package contract

import "sort"

func ExtractionResponseSchema() map[string]any {
	return extractionObject(map[string]any{
		"request_id": extractionString(128),
		"overflow":   map[string]any{"type": "boolean"},
		"coverage":   extractionArray(extractionString(128), 2048),
		"excerpts": extractionArray(extractionObject(map[string]any{
			"event_index": map[string]any{"type": "integer", "minimum": 0, "maximum": MaxEvents - 1},
			"start":       map[string]any{"type": "integer", "minimum": 0},
			"end":         map[string]any{"type": "integer", "minimum": 1},
			"content":     extractionString(999),
		}), MaxExcerpts),
		"security_signals": extractionArray(extractionObject(map[string]any{
			"segment_ref": extractionString(128), "kind": extractionEnum([]string{"prompt_injection", "exfiltration", "hidden_control_markup"}),
		}), 64),
	})
}

func extractionObject(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for key := range properties {
		required = append(required, key)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

func extractionString(limit int) map[string]any {
	return map[string]any{"type": "string", "maxLength": limit}
}

func extractionEnum(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func extractionArray(items map[string]any, limit int) map[string]any {
	return map[string]any{"type": "array", "items": items, "maxItems": limit}
}
