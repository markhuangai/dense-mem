package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func rememberTool(deps Dependencies) Tool {
	return Tool{
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
		NormalizeInput: normalizeRememberInput,
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			input = normalizeRememberInput(input)
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

func normalizeRememberInput(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	delete(out, "claims")
	delete(out, "auto_promote")
	if _, ok := out["evidence"]; ok {
		delete(out, "content")
		delete(out, "source")
		delete(out, "idempotency_key")
		delete(out, "labels")
		delete(out, "metadata")
		return out
	}
	content, ok := out["content"]
	if !ok {
		return out
	}
	evidence := map[string]any{"content": content}
	for _, key := range []string{"source", "idempotency_key", "labels", "metadata"} {
		if value, ok := out[key]; ok {
			evidence[key] = value
			delete(out, key)
		}
	}
	delete(out, "content")
	out["evidence"] = []any{evidence}
	return out
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

func disputeMemoryPlacementTool(deps Dependencies) Tool {
	return Tool{
		Name:        "dispute_memory_placement",
		Description: "Dispute a Dense-Mem placement by supplying evidence for the verifier to review. The dispute session ends when the verifier accepts a correction/promotion or explains why the placement remains rejected or unsupported.",
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
				"message":           schemaString("Client explanation of the dispute.", 1000),
				"evidence":          evidenceArraySchema(),
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
				return nil, fmt.Errorf("dispute_memory_placement: invalid input: %w", err)
			}
			if err := validateDisputeRequest(req); err != nil {
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

func validateDisputeRequest(req memoryservice.DisputeRequest) error {
	if strings.TrimSpace(req.DisputeID) == "" && strings.TrimSpace(req.IngestID) == "" {
		return errors.New("dispute_memory_placement: ingest_id or dispute_id is required")
	}
	if strings.TrimSpace(req.Message) != "" {
		return nil
	}
	for _, evidence := range req.Evidence {
		if strings.TrimSpace(evidence.Content) != "" {
			return nil
		}
	}
	return errors.New("dispute_memory_placement: message or evidence is required")
}

func importMemoriesTool(deps Dependencies) Tool {
	return Tool{
		Name:        "import_memories",
		Description: "Use for summarized historical conversations or migrated memory bundles, not normal live chat turns. Import one granular summarized historical memory entry as evidence and optional typed personal-memory claims. Split bulk history into entries under 1000 characters so claims can attach to precise support. Bulk imports do not auto-promote unless auto_promote is true.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"summary"},
			"properties": map[string]any{
				"summary":         memoryEntryString("Summarized historical conversation or memory bundle."),
				"source":          schemaString("Free-form provenance.", 256),
				"idempotency_key": schemaString("Dedupe key scoped to profile.", 128),
				"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 64}, "maxItems": 20},
				"metadata":        map[string]any{"type": "object", "additionalProperties": true},
				"claims":          typedClaimsSchema(),
				"auto_promote":    map[string]any{"type": "boolean", "description": "Defaults to false."},
			},
			"additionalProperties": false,
		},
		OutputSchema:   memoryResultSchema(),
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			var req memoryservice.ImportRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("import_memories: invalid input: %w", err)
			}
			res, err := deps.Memory.ImportMemories(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func reflectMemoriesTool(deps Dependencies) Tool {
	return Tool{
		Name:        "reflect_memories",
		Description: "Use when memory health may affect the answer or when recall returns clarifications. Review the caller profile's current facts, candidate/disputed claims, stale facts, and clarification needs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":            map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				"stale_after_days": map[string]any{"type": "integer", "minimum": 1},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			var req memoryservice.ReflectRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("reflect_memories: invalid input: %w", err)
			}
			res, err := deps.Memory.Reflect(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func confirmMemoryTool(deps Dependencies) Tool {
	return Tool{
		Name:        "confirm_memory",
		Description: "Apply the user's answer to a memory clarification. Use after the host asks whether to accept the candidate claim or keep existing memory.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"claim_id", "decision"},
			"properties": map[string]any{
				"claim_id": schemaString("Disputed claim ID from a clarification task.", 128),
				"decision": schemaEnum([]string{"accept_claim", "keep_existing", "reject_claim"}),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Memory == nil {
				return nil, ErrToolUnavailable
			}
			var req memoryservice.ConfirmRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("confirm_memory: invalid input: %w", err)
			}
			if req.ClaimID == "" {
				return nil, errors.New("confirm_memory: claim_id is required")
			}
			if req.Decision == "" {
				return nil, errors.New("confirm_memory: decision is required")
			}
			res, err := deps.Memory.ConfirmMemory(ctx, profileID, req)
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
				"source":          schemaString("Free-form provenance.", 256),
				"idempotency_key": schemaString("Dedupe key scoped to profile.", 128),
				"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 64}, "maxItems": 20},
				"metadata":        map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": false,
		},
	}
}

func typedClaimsSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":     "object",
			"required": []string{"subject", "predicate", "object", "extract_conf", "resolution_conf"},
			"properties": map[string]any{
				"subject":            schemaString("Claim subject.", 256),
				"predicate":          schemaEnum([]string{"prefers", "identity_is", "profile_fact", "works_on", "has_goal", "corrected", "has_skill", "knows", "relationship_to", "uses", "likes", "works_at"}),
				"object":             schemaString("Claim object.", 1024),
				"modality":           schemaEnum([]string{"assertion", "question", "proposal", "speculation", "quoted"}),
				"polarity":           schemaEnum([]string{"+", "-"}),
				"speaker":            schemaString("Speaker of the claim.", 256),
				"extract_conf":       map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"resolution_conf":    map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"idempotency_key":    schemaString("Claim dedupe key scoped to profile.", 128),
				"valid_from":         map[string]any{"type": "string", "format": "date-time"},
				"valid_to":           map[string]any{"type": "string", "format": "date-time"},
				"supported_by":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"extraction_model":   schemaString("Extractor model.", 128),
				"extraction_version": schemaString("Extractor version.", 64),
				"pipeline_run_id":    schemaString("Pipeline run id.", 128),
				"classification":     map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": false,
		},
	}
}

func memoryResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fragment":       map[string]any{"type": "object"},
			"claims":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"clarifications": clarificationArraySchema(),
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
