package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
)

func recallFeedbackOutputSchema() map[string]any {
	return closedObject(
		[]string{"recorded", "recorded_count"},
		map[string]any{
			"recorded":        map[string]any{"type": "boolean"},
			"recorded_count":  map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
			"partial_success": map[string]any{"type": "boolean"},
			"failed_index":    map[string]any{"type": "integer", "minimum": 0, "maximum": 19},
			"error":           schemaString("Bounded feedback failure summary.", 128),
			"error_code":      schemaEnum([]string{"degraded", "invalid_input", "internal_failure"}),
			"reason_code":     schemaString("Code-specific bounded failure reason.", 128),
			"next_action":     schemaEnum([]string{"correct_and_resubmit", "retry_same_request", "contact_operator", "stop"}),
			"remediation":     schemaString("Bounded action for the remaining feedback items.", 512),
		},
	)
}

// TerminalRememberOutputSchema exposes the generic terminal Remember schema
// for test-only registry composition without widening the active catalog.
func TerminalRememberOutputSchema() map[string]any {
	return terminalRememberOutputSchema(domain.ContractVersion)
}

func exportMemoryPackOutputSchema() map[string]any {
	return closedObject(
		[]string{"artifact_json", "content_sha256", "filename", "counts", "omissions"},
		map[string]any{
			"artifact_json":  schemaString("Canonical dense-mem.memory-pack.v2.4 JSON.", memoryPackArtifactMaxLength),
			"content_sha256": sHA256Schema(),
			"filename":       schemaString("Suggested artifact filename.", 256),
			"counts":         countMapSchema(),
			"omissions":      array(memoryPackOmissionSchema(), 0, 500),
		},
	)
}

func memoryPackOmissionSchema() map[string]any {
	return closedObject(
		[]string{"item_id", "reason"},
		map[string]any{
			"item_id": schemaString("Artifact-local item ID.", 128),
			"reason":  schemaString("Bounded omission reason.", 1000),
		},
	)
}

func countMapSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "integer", "minimum": 0},
		"maxProperties":        32,
		"x-bounded-map":        true,
	}
}

func sHA256Schema() map[string]any {
	return map[string]any{
		"type":      "string",
		"minLength": 64,
		"maxLength": 64,
		"pattern":   "^[a-f0-9]{64}$",
	}
}
