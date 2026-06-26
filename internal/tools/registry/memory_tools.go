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
		Description: "Use after the user states a durable preference, correction, identity/profile fact, project decision, active goal, reusable instruction, or milestone that should persist across sessions. Store one granular chat-session memory evidence entry, create host-extracted typed personal-memory claims, verify them, and promote non-conflicting validated claims to facts. Do not store temporary task state, speculation, or one-off details as active memory. Keep each entry under 1000 characters and split large scenarios into precise supporting evidence pieces.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"content"},
			"properties": map[string]any{
				"content":         memoryEntryString("Evidence text from the current conversation."),
				"source":          schemaString("Free-form provenance.", 256),
				"idempotency_key": schemaString("Dedupe key scoped to profile.", 128),
				"labels":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 64}, "maxItems": 20},
				"metadata":        map[string]any{"type": "object", "additionalProperties": true},
				"claims":          typedClaimsSchema(),
				"auto_promote":    map[string]any{"type": "boolean", "description": "Defaults to true."},
			},
			"additionalProperties": false,
		},
		OutputSchema:   memoryResultSchema(),
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
