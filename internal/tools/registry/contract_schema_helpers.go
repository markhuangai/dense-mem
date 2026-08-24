package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const (
	memoryPackArtifactMaxLength = 4 * 1024 * 1024
	metadataMaxProperties       = 50
)

func closedObject(required []string, properties map[string]any) map[string]any {
	requireNonEmptyStrings(required, properties)
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

func nonEmptyStringSchema(description string, maxLength int) map[string]any {
	schema := schemaString(description, maxLength)
	schema["minLength"] = 1
	return schema
}

func uuidStringSchema(description string) map[string]any {
	schema := schemaString(description, 128)
	schema["format"] = "uuid"
	schema["x-enforce-format"] = true
	return schema
}

func requireNonEmptyStrings(required []string, properties map[string]any) {
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

func array(items map[string]any, minItems int, maxItems int) map[string]any {
	schema := map[string]any{"type": "array", "items": items}
	if minItems > 0 {
		schema["minItems"] = minItems
	}
	if maxItems > 0 {
		schema["maxItems"] = maxItems
	}
	return schema
}

func nullableString(description string, maxLength int) map[string]any {
	schema := map[string]any{
		"type":        []any{"string", "null"},
		"description": description,
	}
	if maxLength > 0 {
		schema["maxLength"] = maxLength
	}
	return schema
}

func nullableDateTime(description string) map[string]any {
	return map[string]any{
		"type":             []any{"string", "null"},
		"format":           "date-time",
		"x-enforce-format": true,
		"description":      description,
	}
}

func nullableNumber(description string, minimum float64, maximum float64) map[string]any {
	return map[string]any{
		"type":        []any{"number", "null"},
		"minimum":     minimum,
		"maximum":     maximum,
		"description": description,
	}
}

func boundedMap(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          description,
		"maxProperties":        metadataMaxProperties,
		"additionalProperties": true,
		"x-max-depth":          4,
		"x-max-bytes":          16384,
	}
}

func PublicErrorSchema() map[string]any {
	return closedObject(
		[]string{"code", "message", "retryable", "correlation_id"},
		map[string]any{
			"code":                schemaEnum(publicErrorCodes()),
			"message":             schemaString("Bounded public error message.", 512),
			"retryable":           map[string]any{"type": "boolean"},
			"retry_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 86400},
			"correlation_id":      schemaString("Request correlation ID.", 128),
			"details":             boundedMap("Code-specific bounded safe metadata."),
		},
	)
}

func publicErrorCodes() []string {
	return []string{
		string(domain.ErrorInvalidInput),
		string(domain.ErrorUnauthorizedScope),
		string(domain.ErrorWrongOwner),
		string(domain.ErrorConflict),
		string(domain.ErrorProviderUnavailable),
		string(domain.ErrorProviderMalformed),
		string(domain.ErrorDegraded),
	}
}

func submissionStatusErrorArraySchema() map[string]any {
	return array(submissionStatusErrorSchema(), 0, 50)
}

func submissionStatusDegradationArraySchema() map[string]any {
	return array(closedObject(
		[]string{"frontier", "optional", "code", "message"},
		map[string]any{
			"frontier": schemaEnum([]string{"search"}),
			"optional": map[string]any{"type": "boolean"},
			"code":     schemaEnum([]string{string(memoryservice.SubmissionErrorSearchIndexingDelayed)}),
			"message":  schemaString("Bounded optional submission degradation.", 512),
		},
	), 0, 10)
}

func submissionRelationshipResultsSchema() map[string]any {
	return array(closedObject(
		[]string{"ref", "disposition", "splits"},
		map[string]any{
			"ref":         schemaString("Client-local relationship reference.", 128),
			"disposition": schemaEnum([]string{"stored", "not_stored"}),
			"reason":      schemaString("Bounded server disposition reason.", 256),
			"splits": array(closedObject(
				[]string{"split_index", "relationship_id", "relationship_version", "status"},
				map[string]any{
					"split_index":          map[string]any{"type": "integer", "minimum": 0},
					"relationship_id":      schemaString("Canonical Relationship ID.", 128),
					"relationship_version": map[string]any{"type": "integer", "minimum": 1},
					"status":               schemaString("Canonical Relationship status.", 64),
				},
			), 0, 50),
		},
	), 0, 200)
}

func submissionStatusErrorSchema() map[string]any {
	return closedObject(
		[]string{"code", "message", "retryable", "next_action", "remediation"},
		map[string]any{
			"code":        schemaEnum(memoryservice.SubmissionErrorCodes()),
			"message":     schemaString("Bounded safe submission error.", 512),
			"retryable":   map[string]any{"type": "boolean"},
			"next_action": schemaEnum(memoryservice.SubmissionNextActions()),
			"remediation": schemaString("Bounded action the caller can take next.", 512),
		},
	)
}
