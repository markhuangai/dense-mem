package contract

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"sort"
)

func ExtractionResponseSchema() map[string]any {
	value := extractionObject(map[string]any{
		"type":  extractionEnum(domain.ValueTypes()),
		"value": map[string]any{"anyOf": []any{map[string]any{"type": "string", "maxLength": 4096}, map[string]any{"type": "number"}, map[string]any{"type": "boolean"}}},
		"unit":  extractionString(128), "display": extractionString(4096),
	})
	return extractionObject(map[string]any{
		"request_id": extractionString(128),
		"overflow":   map[string]any{"type": "boolean"},
		"coverage":   extractionArray(extractionString(128), 2048),
		"entities": extractionArray(extractionObject(map[string]any{
			"ref": extractionString(64), "name": extractionString(256), "entity_kind": extractionEnum(domain.EntityKinds()),
		}), 400),
		"relationships": extractionArray(extractionObject(map[string]any{
			"ref": extractionString(64), "subject_ref": extractionString(64), "predicate": extractionString(128),
			"object_ref": extractionNullable(extractionString(64)), "object_value": extractionNullable(value),
			"polarity":           extractionEnum([]string{"+", "-"}),
			"citations":          extractionArray(extractionObject(map[string]any{"start_ref": extractionString(128), "end_ref": extractionString(128)}), 20),
			"known_evidence_ids": extractionArray(extractionString(128), 20),
			"valid_from":         extractionNullable(extractionString(128)), "valid_to": extractionNullable(extractionString(128)),
		}), 200),
		"security_signals": extractionArray(extractionObject(map[string]any{
			"segment_ref": extractionString(128), "kind": extractionEnum([]string{"prompt_injection", "exfiltration", "hidden_control_markup"}),
		}), 64),
	})
}

func LinkingResponseSchema() map[string]any {
	return extractionObject(map[string]any{
		"request_id": extractionString(128),
		"groups": extractionArray(extractionObject(map[string]any{
			"ref": extractionString(64), "canonical_ref": extractionString(128),
			"members": extractionArray(extractionString(128), 3200),
		}), 400),
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
func extractionNullable(schema map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
}
