package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func rememberTool(deps Dependencies) Tool {
	return Tool{
		Name:        "remember",
		Description: "Store durable knowledge as raw evidence plus atomic graph proposals. Propose the smallest stable entities and one open-vocabulary semantic relationship per assertion; Dense-Mem independently splits, resolves, verifies, promotes, reviews, rejects, or quarantines each relationship.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"evidence", "proposal"},
			"properties": map[string]any{
				"evidence":       evidenceArraySchema(),
				"proposal":       memoryProposalSchema(),
				"migration_refs": legacyMemoryRefsSchema(),
			},
			"additionalProperties": false,
		},
		OutputSchema:   rememberPlacementResultSchema(),
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
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
}

func legacyMemoryRefsSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Manager-only legacy memories decomposed by this placement. Each accepted bundle is linked with DECOMPOSED_INTO provenance.",
		"minItems":    1,
		"maxItems":    20,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"type", "id"},
			"properties": map[string]any{
				"type": schemaEnum([]string{"fragment", "claim", "fact", "dream"}),
				"id":   schemaString("Team-scoped legacy memory ID.", 256),
			},
			"additionalProperties": false,
		},
	}
}

func resolveMemoryPlacementTool(deps Dependencies) Tool {
	return Tool{
		Name:        "resolve_memory_placement",
		Description: "Acknowledge a completed placement or resolve one ambiguous assertion after confirming with the user. Accept promotes the reviewed assertion using user authority; reject preserves it as rejected history; correct submits authoritative correction evidence and a replacement atomic proposal for normal re-verification.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"ingest_id", "decision"},
			"properties": map[string]any{
				"ingest_id":          schemaString("Placement run ID returned by remember.", 128),
				"placement_item_id":  schemaString("Assertion item to resolve; optional only when exactly one item needs review.", 128),
				"decision":           schemaEnum([]string{"acknowledge", "accept", "reject", "correct"}),
				"corrected_proposal": memoryProposalSchema(),
				"evidence":           evidenceArraySchema(),
			},
			"additionalProperties": false,
		},
		OutputSchema:   placementStatusSchema(),
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			resolver, ok := deps.Memory.(memoryservice.PlacementResolver)
			if !ok {
				return nil, ErrToolUnavailable
			}
			var req memoryservice.ResolvePlacementRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("resolve_memory_placement: invalid input: %w", err)
			}
			if req.Decision == "correct" && (req.CorrectedProposal == nil || len(req.Evidence) == 0) {
				return nil, errors.New("resolve_memory_placement: correct requires corrected_proposal and evidence")
			}
			res, err := resolver.ResolveMemoryPlacement(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
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
				"source_group":    schemaString("Stable independent-source identifier. Two models reading one source must use the same group.", 256),
				"idempotency_key": schemaString("Dedupe key scoped to profile.", 128),
				"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 64}, "maxItems": 20},
				"metadata":        map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": false,
		},
	}
}

func memoryProposalSchema() map[string]any {
	entity := map[string]any{
		"type":     "object",
		"required": []string{"ref", "name", "type"},
		"properties": map[string]any{
			"ref":     schemaString("Request-local entity reference.", 128),
			"name":    schemaString("Smallest canonical entity name.", 256),
			"type":    schemaString("Open machine-readable entity type.", 64),
			"aliases": map[string]any{"type": "array", "maxItems": 20, "items": schemaString("Entity alias.", 256)},
		},
		"additionalProperties": false,
	}
	value := map[string]any{
		"type":     "object",
		"required": []string{"type", "value"},
		"properties": map[string]any{
			"type":    schemaEnum([]string{"string", "number", "boolean", "date", "date_time"}),
			"value":   schemaString("Canonical scalar value.", 1024),
			"display": schemaString("Optional display form.", 1024),
			"unit":    schemaString("Optional scalar unit.", 64),
		},
		"additionalProperties": false,
	}
	evidenceRef := map[string]any{
		"type":     "object",
		"required": []string{"evidence_index", "start", "end"},
		"properties": map[string]any{
			"evidence_index": map[string]any{"type": "integer", "minimum": 0},
			"start":          map[string]any{"type": "integer", "minimum": 0, "description": "Inclusive zero-based Unicode code-point offset."},
			"end":            map[string]any{"type": "integer", "minimum": 1, "description": "Exclusive zero-based Unicode code-point offset."},
		},
		"additionalProperties": false,
	}
	relationship := map[string]any{
		"type":     "object",
		"required": []string{"proposal_id", "subject_ref", "predicate", "policy_family", "polarity", "modality", "evidence"},
		"oneOf": []any{
			map[string]any{"required": []string{"object_ref"}, "not": map[string]any{"required": []string{"object_value"}}},
			map[string]any{"required": []string{"object_value"}, "not": map[string]any{"required": []string{"object_ref"}}},
		},
		"properties": map[string]any{
			"proposal_id":    schemaString("Stable request-local assertion identifier.", 128),
			"subject_ref":    schemaString("Subject entity ref.", 128),
			"predicate":      schemaString("Open lower_snake_case semantic relationship, such as works_on or demoed.", 64),
			"object_ref":     schemaString("Object entity ref.", 128),
			"object_value":   value,
			"policy_family":  schemaEnum([]string{"event_append_only", "multi_state", "single_state", "versioned"}),
			"polarity":       schemaEnum([]string{"+", "-"}),
			"modality":       schemaEnum([]string{"assertion", "question", "proposal", "speculation", "quoted"}),
			"valid_from":     map[string]any{"type": "string", "format": "date-time"},
			"valid_to":       map[string]any{"type": "string", "format": "date-time"},
			"evidence":       map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": evidenceRef},
			"client_comment": schemaString("Optional non-authoritative extraction note.", 500),
		},
		"additionalProperties": false,
	}
	return map[string]any{
		"type":     "object",
		"required": []string{"entities", "relationships"},
		"properties": map[string]any{
			"entities":      map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": entity},
			"relationships": map[string]any{"type": "array", "minItems": 1, "maxItems": 200, "items": relationship},
		},
		"additionalProperties": false,
	}
}

func rememberPlacementResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ingest_id":           map[string]any{"type": "string"},
			"status":              schemaEnum([]string{"queued", "processing", "awaiting_review", "completed", "failed"}),
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

func clarificationArraySchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":                map[string]any{"type": "string"},
				"type":              map[string]any{"type": "string"},
				"question":          map[string]any{"type": "string"},
				"claim_id":          map[string]any{"type": "string"},
				"candidate":         map[string]any{"type": "object"},
				"conflicting_facts": map[string]any{"type": "array", "items": factObjectSchema()},
				"options":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	}
}
