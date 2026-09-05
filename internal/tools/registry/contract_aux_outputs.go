package registry

import (
	"github.com/markhuangai/dense-mem/internal/domain"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
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

func listDreamsOutputSchema() map[string]any {
	return closedObject(
		[]string{"dreams"},
		map[string]any{
			"dreams":      array(hypothesisSummarySchema(), 0, 100),
			"next_cursor": nullableString("Opaque next-page cursor.", 512),
		},
	)
}

func getDreamOutputSchema() map[string]any {
	return closedObject(
		[]string{"hypothesis"},
		map[string]any{"hypothesis": hypothesisSchema()},
	)
}

func resolveDreamFeedbackOutputSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			closedObject(
				[]string{"hypothesis_id", "status"},
				map[string]any{
					"hypothesis_id": schemaString("Hypothesis ID.", 128),
					"status":        schemaEnum(domain.HypothesisStatuses()),
					"submission_id": schemaString("Submission ID for submitted evidence.", 128),
				},
			),
			dreamTerminalRememberOutputSchema(domain.ContractVersion),
			dreamConfirmationBusyOutputSchema(),
		},
	}
}

func dreamConfirmationBusyOutputSchema() map[string]any {
	return closedObject(
		[]string{"code", "message", "retryable", "next_action", "remediation"},
		map[string]any{
			"code":        schemaEnum([]string{"dream_confirmation_busy"}),
			"message":     schemaString("Bounded Dream confirmation admission error.", 512),
			"retryable":   map[string]any{"type": "boolean"},
			"next_action": schemaEnum([]string{string(rememberapp.TerminalNextActionRetryDreamFeedback)}),
			"remediation": schemaString("Bounded action the caller can take next.", 512),
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
