//go:build evaluation

package registry

import (
	"context"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

const evalDreamCycleMaxOutputs = 10000

func evalRunDreamCycleTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_run_dream_cycle",
		Description: "Run an isolated manual dream phase for a team during evaluation so expected dream hypotheses can be exported and scored.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"manual":      map[string]any{"type": "boolean", "description": "Ignored; evaluation dream cycles are always manual."},
				"max_outputs": map[string]any{"type": "integer", "minimum": 1, "maximum": evalDreamCycleMaxOutputs},
				"seed_dreams": map[string]any{
					"type":     "array",
					"maxItems": evalDreamCycleMaxOutputs,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"hypothesis":       map[string]any{"type": "string", "minLength": 1},
							"what_if":          map[string]any{"type": "string"},
							"possible_outcome": map[string]any{"type": "string"},
							"rationale":        map[string]any{"type": "string"},
							"likelihood":       map[string]any{"type": "number", "minimum": 0, "maximum": 1},
							"confidence":       map[string]any{"type": "number", "minimum": 0, "maximum": 1},
							"source_refs": map[string]any{
								"type":     "array",
								"minItems": 1,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"type": map[string]any{"type": "string", "enum": []string{"relationship", "candidate_relationship"}},
										"id":   map[string]any{"type": "string", "minLength": 1},
									},
									"required":             []string{"type", "id"},
									"additionalProperties": false,
								},
							},
						},
						"required":             []string{"hypothesis", "source_refs"},
						"additionalProperties": false,
					},
				},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Dreams == nil {
				return nil, ErrToolUnavailable
			}
			maxOutputs := intInputOrDefault(input["max_outputs"], dreamservice.DefaultMaxOutputs)
			if err := auditEvaluationTool(ctx, deps, "eval_run_dream_cycle", maxOutputs, false, nil); err != nil {
				return nil, err
			}
			req := dreamservice.RunCycleRequest{
				Manual:     true,
				MaxOutputs: maxOutputs,
				SeedDreams: seedDreamsInput(input["seed_dreams"]),
			}
			result, err := deps.Dreams.RunCycle(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			out, err := structToMap(result)
			if err != nil {
				return nil, err
			}
			return out, nil
		},
	}
}

func evalListDreams(ctx context.Context, deps Dependencies, profileID string, input map[string]any, limit int, metadataOnly bool) (map[string]any, error) {
	if deps.Dreams == nil {
		return nil, ErrToolUnavailable
	}
	opts := dreamservice.ListOptions{Limit: limit}
	if cursor, ok := input["cursor"].(string); ok {
		opts.Cursor = cursor
	}
	if status, ok := input["status"].(string); ok {
		opts.Status = status
	}
	dreams, nextCursor, err := deps.Dreams.List(ctx, profileID, opts)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(dreams))
	for _, dream := range dreams {
		item, err := structToMap(dream)
		if err != nil {
			return nil, err
		}
		if metadataOnly {
			stripEvalContent("dream", item)
		}
		items = append(items, item)
	}
	return evalPage(items, nextCursor), nil
}

func dreamRefs(dreams []*domain.Dream) []map[string]any {
	refs := make([]map[string]any, 0, len(dreams))
	for i, dream := range dreams {
		if dream == nil || strings.TrimSpace(dream.DreamID) == "" {
			continue
		}
		refs = append(refs, map[string]any{
			"rank":   i + 1,
			"type":   "dream",
			"id":     dream.DreamID,
			"status": string(dream.Status),
		})
	}
	return refs
}

func seedDreamsInput(value any) []dreamservice.SeedDream {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]dreamservice.SeedDream, 0, len(raw))
	for _, item := range raw {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		seed := dreamservice.SeedDream{
			Hypothesis:      strings.TrimSpace(stringInput(fields["hypothesis"])),
			WhatIf:          strings.TrimSpace(stringInput(fields["what_if"])),
			PossibleOutcome: strings.TrimSpace(stringInput(fields["possible_outcome"])),
			Rationale:       strings.TrimSpace(stringInput(fields["rationale"])),
			SourceRefs:      seedDreamSourceRefsInput(fields["source_refs"]),
		}
		if value, ok := schemaNumber(fields["likelihood"]); ok {
			seed.Likelihood = value
		}
		if value, ok := schemaNumber(fields["confidence"]); ok {
			seed.Confidence = value
		}
		out = append(out, seed)
	}
	return out
}

func seedDreamSourceRefsInput(value any) []domain.DreamSourceRef {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]domain.DreamSourceRef, 0, len(raw))
	for _, item := range raw {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		ref := domain.DreamSourceRef{
			Type: strings.TrimSpace(stringInput(fields["type"])),
			ID:   strings.TrimSpace(stringInput(fields["id"])),
		}
		if ref.Type != "" && ref.ID != "" {
			out = append(out, ref)
		}
	}
	return out
}
