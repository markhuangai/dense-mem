package registry

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func findSkillPackCandidatesTool(deps Dependencies) Tool {
	return Tool{
		Name:        "find_skill_pack_candidates",
		Description: "Find existing facts and validated claims that could be exported into a portable skill-pack artifact.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query": schemaString("Topic search query.", 512),
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.SkillPack == nil {
				return nil, ErrToolUnavailable
			}
			var req skillpackservice.FindCandidatesRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("find_skill_pack_candidates: invalid input: %w", err)
			}
			res, err := deps.SkillPack.FindCandidates(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func exportSkillPackTool(deps Dependencies) Tool {
	return Tool{
		Name:        "export_skill_pack",
		Description: "Export selected facts, validated claims, and manual triples into canonical dense-mem skill-pack JSON plus a SHA-256 integrity hash.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":         schemaString("Skill pack name.", 256),
				"description":  schemaString("Short skill pack description.", 1024),
				"fact_ids":     map[string]any{"type": "array", "items": schemaString("Fact ID.", 128)},
				"claim_ids":    map[string]any{"type": "array", "items": schemaString("Claim ID.", 128)},
				"manual_items": skillPackItemsSchema(),
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.SkillPack == nil {
				return nil, ErrToolUnavailable
			}
			var req skillpackservice.ExportRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("export_skill_pack: invalid input: %w", err)
			}
			res, err := deps.SkillPack.Export(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func inspectSkillPackTool(deps Dependencies) Tool {
	return Tool{
		Name:        "inspect_skill_pack",
		Description: "Parse a skill-pack artifact or HTTPS URL, verify its optional SHA-256 hash, and report duplicates and conflicts without writing memory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"artifact":            skillPackSchema(),
				"artifact_json":       schemaString("Skill pack JSON string.", 0),
				"url":                 schemaString("HTTPS URL to a skill pack JSON artifact.", 2048),
				"expected_sha256":     schemaString("Expected canonical artifact SHA-256.", 64),
				"recommend_decisions": map[string]any{"type": "boolean", "description": "Ask the configured LLM decider to recommend actions for conflicts without writing memory."},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.SkillPack == nil {
				return nil, ErrToolUnavailable
			}
			var req skillpackservice.InspectRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("inspect_skill_pack: invalid input: %w", err)
			}
			res, err := deps.SkillPack.Inspect(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func importSkillPackTool(deps Dependencies) Tool {
	return Tool{
		Name:        "import_skill_pack",
		Description: "Import a skill pack in review or trusted mode. Trusted URL imports require a SHA-256 hash and conflict decisions before local facts are superseded.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"mode"},
			"properties": map[string]any{
				"artifact":              skillPackSchema(),
				"artifact_json":         schemaString("Skill pack JSON string.", 0),
				"url":                   schemaString("HTTPS URL to a skill pack JSON artifact.", 2048),
				"expected_sha256":       schemaString("Expected canonical artifact SHA-256.", 64),
				"mode":                  schemaEnum([]string{"review", "trusted"}),
				"selected_items":        map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 0}},
				"conflict_decisions":    conflictDecisionsSchema(),
				"auto_decide_conflicts": map[string]any{"type": "boolean", "description": "Allow the configured LLM decider to fill missing conflict decisions before trusted import writes."},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.SkillPack == nil {
				return nil, ErrToolUnavailable
			}
			var req skillpackservice.ImportRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("import_skill_pack: invalid input: %w", err)
			}
			res, err := deps.SkillPack.Import(ctx, profileID, req)
			if err != nil {
				if res != nil && res.ImportID != "" {
					if res.Status == "" {
						res.Status = "error"
					}
					if res.Error == "" {
						res.Error = err.Error()
					}
					return structToMap(res)
				}
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func rollbackSkillPackImportTool(deps Dependencies) Tool {
	return Tool{
		Name:        "rollback_skill_pack_import",
		Description: "Rollback an applied skill-pack import using the Postgres import ledger. Rollback aborts when affected graph entities changed after import.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"import_id"},
			"properties":           map[string]any{"import_id": schemaString("Skill pack import ID.", 128)},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.SkillPack == nil {
				return nil, ErrToolUnavailable
			}
			var req skillpackservice.RollbackRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("rollback_skill_pack_import: invalid input: %w", err)
			}
			res, err := deps.SkillPack.Rollback(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		},
	}
}

func skillPackSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"schema_version", "name", "items"},
		"properties": map[string]any{
			"schema_version": schemaEnum([]string{"dense-mem.skill_pack.v1"}),
			"name":           schemaString("Skill pack name.", 256),
			"description":    schemaString("Short skill pack description.", 1024),
			"items":          skillPackItemsSchema(),
		},
		"additionalProperties": false,
	}
}

func skillPackItemsSchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"subject", "predicate", "object", "source_kind"},
			"properties": map[string]any{
				"subject":     schemaString("Triple subject.", 256),
				"predicate":   schemaEnum([]string{"has_skill", "knows", "uses"}),
				"object":      schemaString("Triple object.", 1024),
				"source_kind": schemaEnum([]string{"source_fact", "source_validated_claim", "manual"}),
			},
			"additionalProperties": false,
		},
	}
}

func conflictDecisionsSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":     "object",
			"required": []string{"index", "action"},
			"properties": map[string]any{
				"index":  map[string]any{"type": "integer", "minimum": 0},
				"action": schemaEnum([]string{"import_anyway", "skip", "supersede_local", "demote_to_claim"}),
			},
			"additionalProperties": false,
		},
	}
}
