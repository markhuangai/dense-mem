package registry

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const defaultRecallCaseLimit = 10

func evalRunRecallCaseTool(deps Dependencies) Tool {
	return Tool{
		Name:        "eval_run_recall_case",
		Description: "Run one recall/context evaluation case through the current Dense-Mem logic and return ranked refs plus context refs.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"case_id", "query"},
			"properties": map[string]any{
				"case_id":           schemaString("Evaluation case ID.", 256),
				"query":             schemaString("Recall query.", 512),
				"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				"valid_at":          map[string]any{"type": "string", "format": "date-time"},
				"known_at":          map[string]any{"type": "string", "format": "date-time"},
				"include_evidence":  map[string]any{"type": "boolean"},
				"use_communities":   map[string]any{"type": "boolean"},
				"include_dreams":    map[string]any{"type": "boolean"},
				"max_context_chars": map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
			},
			"additionalProperties": false,
		},
		OutputSchema:   map[string]any{"type": "object"},
		RequiredScopes: []string{"read", "write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Recall == nil {
				return nil, ErrToolUnavailable
			}
			limit := intInputOrDefault(input["limit"], defaultRecallCaseLimit)
			if err := auditEvaluationTool(ctx, deps, "eval_run_recall_case", limit, false, map[string]any{"case_id": input["case_id"]}); err != nil {
				return nil, err
			}
			return evalRunRecallCase(ctx, deps, input)
		},
	}
}

func evalRunRecallCase(ctx context.Context, deps Dependencies, input map[string]any) (map[string]any, error) {
	req, err := evalRecallRequest(input)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	result, err := deps.Recall.Recall(ctx, req)
	if err != nil {
		return nil, err
	}
	ranked := recallResultRefs(result)
	out := map[string]any{
		"case_id":     input["case_id"],
		"query":       req.Query,
		"ranked_refs": ranked,
		"latency_ms":  time.Since(started).Milliseconds(),
	}
	if result != nil {
		out["recall_id"] = result.RecallID
		out["search_state"] = result.SearchState
		if result.Degradation != nil {
			out["degradation"] = result.Degradation
		}
	}
	if boolInputOrDefault(input["include_evidence"], true) {
		out["context_evidence_refs"] = ranked
	}
	limit := intInputOrDefault(input["limit"], defaultRecallCaseLimit)
	if boolInput(input["include_dreams"]) && deps.Dreams != nil {
		dreams, err := deps.Dreams.Recall(ctx, "", req.Query, limit)
		if err != nil {
			return nil, err
		}
		out["dream_refs"] = dreamRefs(dreams)
	}
	return out, nil
}

func evalRecallRequest(input map[string]any) (memoryservice.RecallRequest, error) {
	var req memoryservice.RecallRequest
	if err := remapInput(input, &req); err != nil {
		return req, err
	}
	req.ContractVersion = domain.ContractVersion
	req.Query = stringInput(input["query"])
	req.Limit = intInputOrDefault(input["limit"], defaultRecallCaseLimit)
	if validAt, err := optionalTime(input["valid_at"]); err != nil {
		return req, err
	} else {
		req.ValidAt = validAt
	}
	if knownAt, err := optionalTime(input["known_at"]); err != nil {
		return req, err
	} else {
		req.KnownAt = knownAt
	}
	return req, nil
}

func recallResultRefs(result *memoryservice.RecallResult) []map[string]any {
	if result == nil {
		return []map[string]any{}
	}
	refs := make([]map[string]any, 0, len(result.Results))
	for _, item := range result.Results {
		if stringInput(item.EvidenceID) == "" {
			continue
		}
		rank := item.Rank
		if rank <= 0 {
			rank = len(refs) + 1
		}
		ref := map[string]any{
			"rank": rank,
			"type": "evidence",
			"id":   item.EvidenceID,
		}
		if len(item.RelationshipIDs) > 0 {
			ref["relationship_ids"] = append([]string(nil), item.RelationshipIDs...)
		}
		refs = append(refs, ref)
	}
	return refs
}
