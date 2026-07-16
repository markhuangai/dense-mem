package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const resolveMemoryPlacementActionReleaseQuarantine = "release_quarantine"

func rememberTool(deps Dependencies) Tool {
	var tool Tool
	tool = Tool{
		Name:        "remember",
		Description: "Use after the user states durable memory evidence such as a preference, correction, project decision, active goal, reusable instruction, or milestone. Submit evidence only; Dense-Mem decides whether it remains a fragment, becomes a claim, becomes a fact, needs more evidence, or is rejected.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"evidence"},
			"properties": map[string]any{
				"evidence": evidenceArraySchema(),
			},
			"additionalProperties": false,
		},
		OutputSchema:   rememberPlacementResultSchema(),
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateInput(tool, input); err != nil {
				return nil, fmt.Errorf("remember: invalid input: %w", err)
			}
			var req memoryservice.RememberRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("remember: invalid input: %w", err)
			}
			res, err := deps.Memory.Remember(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
	return tool
}

func getMemoryPlacementTool(deps Dependencies) Tool {
	return Tool{
		Name:        "get_memory_placement",
		Description: "Poll the Dense-Mem verifier placement result returned by remember. Use after remember returns an ingest_id; wait about check_after_seconds before polling again if status is queued or processing.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ingest_id"},
			"properties": map[string]any{
				"ingest_id": schemaString("Placement run ID returned by remember.", 128),
			},
			"additionalProperties": false,
		},
		OutputSchema:   placementStatusSchema(),
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			var req memoryservice.PlacementStatusRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("get_memory_placement: invalid input: %w", err)
			}
			res, err := deps.Memory.GetMemoryPlacement(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func resolveMemoryPlacementTool(deps Dependencies) Tool {
	return Tool{
		Name:        "resolve_memory_placement",
		Description: "Resolve a Dense-Mem placement by supplying correction evidence or a rejection explanation for verifier review. Managers may also set action=release_quarantine with a quarantined placement item and bounded reason. Dispute sessions end when the verifier accepts the correction or explains why the placement remains rejected or unsupported.",
		InputSchema: map[string]any{
			"type": "object",
			"allOf": []any{
				map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"ingest_id"}},
					map[string]any{"required": []string{"dispute_id"}},
				}},
				map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"message"}},
					map[string]any{"required": []string{"evidence"}},
				}},
			},
			"properties": map[string]any{
				"ingest_id":         schemaString("Placement run ID returned by remember.", 128),
				"placement_item_id": schemaString("Specific placement item ID to dispute.", 128),
				"dispute_id":        schemaString("Existing dispute session ID.", 128),
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{resolveMemoryPlacementActionReleaseQuarantine},
					"description": "Optional manager-only resolution action. Omit for normal dispute review.",
				},
				"message":  schemaString("Client explanation of the dispute.", 1000),
				"evidence": evidenceArraySchema(),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			var req memoryservice.DisputeRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("resolve_memory_placement: invalid input: %w", err)
			}
			if err := validateResolveMemoryPlacementRequest(req); err != nil {
				return nil, err
			}
			res, err := deps.Memory.DisputeMemoryPlacement(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func validateResolveMemoryPlacementRequest(req memoryservice.DisputeRequest) error {
	action := strings.TrimSpace(req.Action)
	if action == resolveMemoryPlacementActionReleaseQuarantine {
		if strings.TrimSpace(req.IngestID) == "" {
			return errors.New("resolve_memory_placement: ingest_id is required for release_quarantine")
		}
		if strings.TrimSpace(req.PlacementItemID) == "" {
			return errors.New("resolve_memory_placement: placement_item_id is required for release_quarantine")
		}
		if strings.TrimSpace(req.Message) == "" {
			return errors.New("resolve_memory_placement: quarantine release reason is required")
		}
		if len([]rune(strings.TrimSpace(req.Message))) > 1000 {
			return errors.New("resolve_memory_placement: quarantine release reason must be at most 1000 characters")
		}
		if len(req.Evidence) > 0 {
			return errors.New("resolve_memory_placement: evidence is not accepted for release_quarantine")
		}
		return nil
	}
	if action != "" {
		return fmt.Errorf("resolve_memory_placement: unsupported action %q", action)
	}
	if strings.TrimSpace(req.DisputeID) == "" && strings.TrimSpace(req.IngestID) == "" {
		return errors.New("resolve_memory_placement: ingest_id or dispute_id is required")
	}
	if strings.TrimSpace(req.Message) != "" {
		return nil
	}
	for _, evidence := range req.Evidence {
		if strings.TrimSpace(evidence.Content) != "" {
			return nil
		}
	}
	return errors.New("resolve_memory_placement: message or evidence is required")
}

func evidenceArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 20,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"content"},
			"properties": map[string]any{
				"content":         memoryEntryString("Evidence text from the current conversation."),
				"source_type":     schemaEnum([]string{"conversation", "document", "observation", "manual"}),
				"source":          schemaString("Free-form provenance.", 256),
				"authority":       schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
				"source_group":    schemaString("Source independence group proposed by caller; server policy decides final grouping.", 256),
				"idempotency_key": schemaString("Dedupe key scoped to profile.", 128),
				"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 64}, "maxItems": 20},
				"metadata":        map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": false,
		},
	}
}

func rememberPlacementResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ingest_id":           map[string]any{"type": "string"},
			"status":              schemaEnum([]string{"queued", "processing", "completed", "failed"}),
			"check_after_seconds": map[string]any{"type": "integer"},
			"status_tool":         map[string]any{"type": "string"},
			"evidence":            map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"items":               map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		},
	}
}

func placementStatusSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"placement": map[string]any{"type": "object"},
		},
	}
}
