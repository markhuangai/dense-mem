package registry

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func v2UATTools(deps Dependencies) []Tool {
	tools := V2ContractTools()
	for i := range tools {
		switch tools[i].Name {
		case V2ToolRemember:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2Remember == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("remember: invalid input: %w", err)
				}
				var req memoryservice.V2RememberRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("remember: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.V2Remember.RememberV2(ctx, req)
				if err != nil {
					return nil, err
				}
				out, err := structToMap(res)
				if err != nil {
					return nil, err
				}
				return v2ProcessingStatePublicOutput(out), nil
			}
		case V2ToolRecallMemory:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2Recall == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
				}
				var req memoryservice.V2RecallRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.V2Recall.RecallV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		case V2ToolGetMemoryPlacement:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2Remember == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("get_memory_placement: invalid input: %w", err)
				}
				var req memoryservice.V2GetMemoryPlacementRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("get_memory_placement: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.V2Remember.GetMemoryPlacementV2(ctx, req)
				if err != nil {
					return nil, err
				}
				out, err := structToMap(res)
				if err != nil {
					return nil, err
				}
				return v2ProcessingStatePublicOutput(out), nil
			}
		case V2ToolResolveMemoryPlacement:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2Lifecycle == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("resolve_memory_placement: invalid input: %w", err)
				}
				var req memoryservice.V2ResolveMemoryPlacementRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("resolve_memory_placement: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				applyV2ResolveMemoryPlacementPublicInput(input, &req)
				res, err := deps.V2Lifecycle.ResolveMemoryPlacementV2(ctx, req)
				if err != nil {
					return nil, err
				}
				out, err := structToMap(res)
				if err != nil {
					return nil, err
				}
				return v2ResolveMemoryPlacementPublicOutput(out), nil
			}
		case V2ToolCorrectEntityResolution:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2Lifecycle == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("correct_entity_resolution: invalid input: %w", err)
				}
				var req memoryservice.V2CorrectEntityResolutionRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("correct_entity_resolution: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				applyV2CorrectEntityResolutionPublicInput(input, &req)
				res, err := deps.V2Lifecycle.CorrectEntityResolutionV2(ctx, req)
				if err != nil {
					return nil, err
				}
				out, err := structToMap(res)
				if err != nil {
					return nil, err
				}
				return v2CorrectEntityResolutionPublicOutput(out), nil
			}
		case V2ToolTraceMemory:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Context == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("trace_memory: invalid input: %w", err)
				}
				var req contextservice.TraceRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("trace_memory: invalid input: %w", err)
				}
				res, err := deps.Context.Trace(ctx, "", req)
				if err != nil {
					return nil, err
				}
				if res != nil && res.V2Semantic != nil {
					return structToMap(res.V2Semantic)
				}
				return structToMap(res)
			}
		}
	}
	return tools
}

func applyV2ResolveMemoryPlacementPublicInput(input map[string]any, req *memoryservice.V2ResolveMemoryPlacementRequest) {
	if reason, ok := input["reason"].(string); ok {
		req.Message = reason
	}
	decision, ok := objectFields(input["decision"])
	if !ok {
		return
	}
	if value, ok := decision["mention_ref"].(string); ok {
		req.EntityRef = value
	}
	if value, ok := decision["entity_id"].(string); ok {
		req.CandidateEntityID = value
	}
	if value, ok := decision["observation_id"].(string); ok {
		req.ObservationID = value
	}
	if value, ok := decision["predicate_key"].(string); ok {
		req.PredicateKey = value
	}
	if value, ok := intInput(decision["predicate_version"]); ok {
		req.PredicateVersion = value
	}
}

func applyV2CorrectEntityResolutionPublicInput(input map[string]any, req *memoryservice.V2CorrectEntityResolutionRequest) {
	if operation, ok := input["operation"].(string); ok {
		req.Action = domain.V2EntityCorrectionAction(operation)
	}
	req.SelectedObservationIDs = v2StringSliceFromInput(input["owned_observation_ids"])
	if token, ok := input["impact_token"].(string); ok {
		req.PlanToken = token
	}
}

func v2StringSliceFromInput(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

func v2ResolveMemoryPlacementPublicOutput(out map[string]any) map[string]any {
	return v2ProcessingStatePublicOutput(out)
}

func v2ProcessingStatePublicOutput(out map[string]any) map[string]any {
	if status, ok := out["status"]; ok {
		out["processing_state"] = status
		delete(out, "status")
	}
	return out
}

func v2CorrectEntityResolutionPublicOutput(out map[string]any) map[string]any {
	if token, ok := out["plan_token"]; ok {
		out["impact_token"] = token
		delete(out, "plan_token")
	}
	if selected, ok := out["selected_ids"]; ok {
		out["selected_observation_ids"] = selected
		delete(out, "selected_ids")
	}
	if blocked, ok := out["blocked_ids"]; ok {
		out["blocked_observation_ids"] = blocked
		delete(out, "blocked_ids")
	}
	if _, ok := out["relationship_changes"]; !ok {
		out["relationship_changes"] = []any{}
	}
	if _, ok := out["entity_candidates"]; !ok {
		out["entity_candidates"] = []any{}
	}
	if _, ok := out["unchanged_cross_profile_references"]; !ok {
		out["unchanged_cross_profile_references"] = []any{}
	}
	delete(out, "impact_summary")
	delete(out, "degradation")
	return out
}
