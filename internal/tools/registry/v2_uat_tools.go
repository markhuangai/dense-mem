package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
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
				recordV2RecallFeedbackSnapshot(ctx, deps, input, req, res)
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
		case V2ToolListDreams:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("list_dreams: invalid input: %w", err)
				}
				opts := dreamservice.ListOptions{}
				if value, ok := input["limit"].(float64); ok {
					opts.Limit = int(value)
				}
				if value, ok := input["status"].(string); ok {
					opts.Status = value
				}
				dreams, next, err := deps.Dreams.List(ctx, profileID, opts)
				if err != nil {
					return nil, err
				}
				return map[string]any{"dreams": v2DreamPublicSummaries(dreams), "next_cursor": next}, nil
			}
		case V2ToolGetDream:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("get_dream: invalid input: %w", err)
				}
				id, _ := input["hypothesis_id"].(string)
				dream, err := deps.Dreams.Get(ctx, profileID, id)
				if err != nil {
					return nil, err
				}
				return map[string]any{"hypothesis": v2DreamPublicHypothesis(dream)}, nil
			}
		case V2ToolResolveDreamFeedback:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
				}
				var req dreamservice.ResolveFeedbackRequest
				if err := remapInput(v2DreamFeedbackServiceInput(input), &req); err != nil {
					return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
				}
				res, err := deps.Dreams.ResolveFeedback(ctx, profileID, req)
				if err != nil {
					return nil, err
				}
				return v2DreamFeedbackPublicOutput(res), nil
			}
		case V2ToolListCommunities:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2Communities == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("list_communities: invalid input: %w", err)
				}
				actor, ok := requestctx.ActorProfileFromContext(ctx)
				if !ok || actor.TeamID == uuid.Nil {
					return nil, fmt.Errorf("list_communities: authenticated actor context is required")
				}
				limit := 0
				if value, ok := input["limit"].(float64); ok {
					limit = int(value)
				}
				communities, err := deps.V2Communities.ListV2Communities(ctx, repository.V2CommunityListInput{
					TeamID: actor.TeamID.String(),
					Limit:  limit,
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"communities": communities}, nil
			}
		case V2ToolFindMemoryPackCandidates:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2SkillPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("find_memory_pack_candidates: invalid input: %w", err)
				}
				var req skillpackservice.V2FindCandidatesRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("find_memory_pack_candidates: invalid input: %w", err)
				}
				res, err := deps.V2SkillPack.FindCandidatesV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2MemoryPackCandidatesPublicOutput(res), nil
			}
		case V2ToolExportMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2SkillPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.V2ExportRequest
				if err := remapInput(v2MemoryPackExportServiceInput(input), &req); err != nil {
					return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
				}
				res, err := deps.V2SkillPack.ExportV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2MemoryPackExportPublicOutput(res), nil
			}
		case V2ToolInspectMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2SkillPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("inspect_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.V2InspectRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("inspect_memory_pack: invalid input: %w", err)
				}
				res, err := deps.V2SkillPack.InspectV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2MemoryPackInspectPublicOutput(res, input), nil
			}
		case V2ToolImportMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2SkillPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("import_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.V2ImportRequest
				if err := remapInput(v2MemoryPackImportServiceInput(input), &req); err != nil {
					return nil, fmt.Errorf("import_memory_pack: invalid input: %w", err)
				}
				res, err := deps.V2SkillPack.ImportV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2MemoryPackImportPublicOutput(res), nil
			}
		case V2ToolRollbackMemoryPackImport:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.V2SkillPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, tool.RequiredScopes); err != nil {
					return nil, fmt.Errorf("rollback_memory_pack_import: invalid input: %w", err)
				}
				var req skillpackservice.V2RollbackRequest
				if err := remapInput(v2MemoryPackRollbackServiceInput(input), &req); err != nil {
					return nil, fmt.Errorf("rollback_memory_pack_import: invalid input: %w", err)
				}
				res, err := deps.V2SkillPack.RollbackV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2MemoryPackRollbackPublicOutput(res), nil
			}
		}
	}
	return tools
}

func v2DreamFeedbackServiceInput(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+2)
	for key, value := range input {
		out[key] = value
	}
	if value, ok := input["hypothesis_id"]; ok {
		out["dream_id"] = value
	}
	if value, ok := input["reason"]; ok {
		out["feedback"] = value
	}
	return out
}

func v2DreamFeedbackPublicOutput(res *dreamservice.ResolveFeedbackResult) map[string]any {
	out := map[string]any{
		"hypothesis_id": "",
		"status":        "",
	}
	if res == nil || res.Dream == nil {
		return out
	}
	out["hypothesis_id"] = res.Dream.DreamID
	out["status"] = string(res.Dream.Status)
	if res.V2Memory != nil && res.V2Memory.IngestID != "" {
		out["ingest_id"] = res.V2Memory.IngestID
	}
	return out
}

func v2DreamPublicSummaries(dreams []*domain.Dream) []any {
	out := make([]any, 0, len(dreams))
	for _, dream := range dreams {
		out = append(out, v2DreamPublicSummary(dream))
	}
	return out
}

func v2DreamPublicSummary(dream *domain.Dream) map[string]any {
	hypothesis := v2DreamPublicHypothesis(dream)
	return map[string]any{
		"hypothesis_id":           hypothesis["hypothesis_id"],
		"subject_entity_id":       hypothesis["subject_entity_id"],
		"predicate_key":           hypothesis["predicate_key"],
		"object_entity_id":        hypothesis["object_entity_id"],
		"object_value_id":         hypothesis["object_value_id"],
		"statement":               hypothesis["statement"],
		"status":                  hypothesis["status"],
		"source_relationship_ids": hypothesis["source_relationship_ids"],
		"generator_kind":          hypothesis["generator_kind"],
		"generator_version":       hypothesis["generator_version"],
		"created_at":              hypothesis["created_at"],
	}
}

func v2DreamPublicHypothesis(dream *domain.Dream) map[string]any {
	out := map[string]any{
		"hypothesis_id":                     "",
		"source_owner_profile_ids":          []string{},
		"subject_entity_id":                 "",
		"predicate_key":                     "",
		"object_entity_id":                  nil,
		"object_value_id":                   nil,
		"statement":                         "",
		"rationale":                         "",
		"likelihood":                        0,
		"confidence":                        0,
		"source_relationship_ids":           []string{},
		"source_candidate_relationship_ids": []string{},
		"source_versions":                   map[string]int{},
		"generator_kind":                    "deterministic",
		"generator_version":                 "unknown",
		"status":                            string(domain.DreamStatusProposed),
		"created_at":                        time.Time{}.UTC().Format(time.RFC3339Nano),
	}
	if dream == nil {
		return out
	}
	out["hypothesis_id"] = dream.DreamID
	out["statement"] = dream.Hypothesis
	out["rationale"] = dream.Rationale
	out["likelihood"] = dream.Likelihood
	out["confidence"] = dream.Confidence
	out["source_relationship_ids"] = v2DreamSourceIDs(dream.SourceRefs, "relationship")
	out["source_candidate_relationship_ids"] = v2DreamSourceIDs(dream.SourceRefs, "candidate_relationship")
	if dream.GeneratorModel != "" {
		out["generator_version"] = dream.GeneratorModel
	}
	out["status"] = string(dream.Status)
	if !dream.CreatedAt.IsZero() {
		out["created_at"] = dream.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func v2DreamSourceIDs(refs []domain.DreamSourceRef, sourceType string) []string {
	out := []string{}
	for _, ref := range refs {
		if ref.Type == sourceType && ref.ID != "" {
			out = append(out, ref.ID)
		}
	}
	return out
}

func v2MemoryPackExportServiceInput(input map[string]any) map[string]any {
	out := v2CopyInput(input)
	if value, ok := input["include_evidence"]; ok {
		out["include_support"] = value
	}
	return out
}

func v2MemoryPackImportServiceInput(input map[string]any) map[string]any {
	out := v2CopyInput(input)
	decisions, ok := input["conflict_decisions"].([]any)
	if !ok {
		return out
	}
	mapped := make([]any, 0, len(decisions))
	for _, raw := range decisions {
		item, ok := raw.(map[string]any)
		if !ok {
			mapped = append(mapped, raw)
			continue
		}
		copy := v2CopyInput(item)
		if value, ok := item["decision"]; ok {
			copy["action"] = value
		}
		mapped = append(mapped, copy)
	}
	out["conflict_decisions"] = mapped
	return out
}

func v2MemoryPackRollbackServiceInput(input map[string]any) map[string]any {
	out := v2CopyInput(input)
	dryRun, _ := input["dry_run"].(bool)
	if !dryRun && input["impact_token"] != nil {
		out["confirm"] = true
	}
	return out
}

func v2MemoryPackCandidatesPublicOutput(res *skillpackservice.V2FindCandidatesResult) map[string]any {
	candidates := []any{}
	if res != nil {
		for i, candidate := range res.Candidates {
			candidates = append(candidates, map[string]any{
				"relationship_id": candidate.RelationshipID,
				"subject":         v2MemoryPackEntityHandle("", candidate.Subject),
				"predicate":       candidate.PredicateKey,
				"object":          v2MemoryPackEntityHandle("", candidate.Object),
				"polarity":        "+",
				"rank":            i + 1,
			})
		}
	}
	return map[string]any{"candidates": candidates}
}

func v2MemoryPackExportPublicOutput(res *skillpackservice.V2ExportResult) map[string]any {
	out := map[string]any{
		"artifact_json":  "",
		"content_sha256": "",
		"filename":       "",
		"counts":         map[string]int{"items": 0},
		"omissions":      []any{},
	}
	if res == nil {
		return out
	}
	out["artifact_json"] = res.CanonicalJSON
	out["content_sha256"] = res.SHA256
	out["filename"] = res.Filename
	out["counts"] = map[string]int{"items": res.ItemCount}
	out["omissions"] = v2MemoryPackStringOmissions(res.Omissions)
	return out
}

func v2MemoryPackInspectPublicOutput(res *skillpackservice.V2InspectResult, input map[string]any) map[string]any {
	mode, _ := input["mode"].(string)
	out := map[string]any{
		"valid":             false,
		"format":            skillpackservice.V2MemoryPackFormat,
		"content_sha256":    "",
		"mode":              mode,
		"counts":            map[string]int{"items": 0},
		"conflicts":         []any{},
		"expected_outcomes": map[string]int{},
	}
	if res == nil {
		return out
	}
	out["valid"] = res.Format == skillpackservice.V2MemoryPackFormat
	out["format"] = res.Format
	out["content_sha256"] = res.ArtifactHash
	out["counts"] = map[string]int{"items": res.ItemCount, "selected": res.SelectedCount}
	out["conflicts"] = v2MemoryPackConflicts(res.DecisionsRequired)
	return out
}

func v2MemoryPackImportPublicOutput(res *skillpackservice.V2ImportResult) map[string]any {
	out := map[string]any{
		"import_id":        "",
		"processing_state": "completed",
		"ingest_ids":       []string{},
		"omissions":        []any{},
	}
	if res == nil {
		return out
	}
	out["import_id"] = res.ImportID
	out["processing_state"] = v2MemoryPackProcessingState(res.Status)
	if res.IngestID != "" {
		out["ingest_ids"] = []string{res.IngestID}
	}
	out["omissions"] = v2MemoryPackImportOmissions(res.Items)
	return out
}

func v2MemoryPackRollbackPublicOutput(res *skillpackservice.V2RollbackResult) map[string]any {
	out := map[string]any{
		"import_id":                 "",
		"dry_run":                   true,
		"safe":                      false,
		"blockers":                  []any{},
		"affected_relationship_ids": []string{},
	}
	if res == nil {
		return out
	}
	out["import_id"] = res.ImportID
	out["dry_run"] = res.DryRun
	out["safe"] = res.Status == "safe" || res.Status == domain.SkillPackImportStatusRolledBack
	out["blockers"] = v2MemoryPackRollbackBlockers(res.Conflicts)
	out["applied"] = !res.DryRun && res.Status == domain.SkillPackImportStatusRolledBack
	return out
}

func v2MemoryPackEntityHandle(entityID string, name string) map[string]any {
	return map[string]any{"entity_id": entityID, "name": name}
}

func v2MemoryPackStringOmissions(values []string) []any {
	out := make([]any, 0, len(values))
	for i, value := range values {
		out = append(out, map[string]any{
			"item_id": fmt.Sprintf("omission-%d", i+1),
			"reason":  value,
		})
	}
	return out
}

func v2MemoryPackConflicts(values []skillpackservice.V2ConflictPrompt) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{
			"item_id":           value.ItemID,
			"kind":              value.Reason,
			"allowed_decisions": value.AllowedActions,
		})
	}
	return out
}

func v2MemoryPackImportOmissions(items []skillpackservice.V2ImportItemResult) []any {
	out := []any{}
	for _, item := range items {
		if item.Status == "skipped" || item.Error != "" {
			reason := item.Error
			if reason == "" {
				reason = item.Status
			}
			out = append(out, map[string]any{"item_id": item.ItemID, "reason": reason})
		}
	}
	return out
}

func v2MemoryPackRollbackBlockers(values []string) []any {
	out := make([]any, 0, len(values))
	for i, value := range values {
		out = append(out, map[string]any{
			"code":    fmt.Sprintf("rollback_blocker_%d", i+1),
			"message": value,
		})
	}
	return out
}

func v2MemoryPackProcessingState(status string) string {
	switch status {
	case "", domain.SkillPackImportStatusApplied, domain.SkillPackImportStatusRolledBack:
		return "completed"
	case "pending":
		return "queued"
	case "running":
		return "processing"
	case "failed":
		return "failed"
	default:
		return "completed"
	}
}

func v2CopyInput(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func recordV2RecallFeedbackSnapshot(
	ctx context.Context,
	deps Dependencies,
	input map[string]any,
	req memoryservice.V2RecallRequest,
	res *memoryservice.V2RecallResult,
) {
	if res == nil || res.RecallID == "" || deps.RecallFeedbackEvents == nil {
		return
	}
	if !RecallFeedbackEnabled(ctx, deps.RecallFeedbackConfig) || deps.Metrics == nil {
		return
	}
	degradation := map[string]any{}
	if res.Degradation != nil {
		if mapped, err := structToMap(res.Degradation); err == nil {
			degradation = mapped
		}
	}
	err := deps.RecallFeedbackEvents.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{
		RecallID:        res.RecallID,
		ToolName:        V2ToolRecallMemory,
		Query:           req.Query,
		ToolArgs:        v2RecallFeedbackToolArgs(input, req),
		ResultRefs:      v2RecallFeedbackResultRefs(res),
		ContractVersion: domain.V2ContractVersion,
		SearchState:     res.SearchState,
		Degradation:     degradation,
		SnapshotMetadata: map[string]any{
			"result_schema": "v2.evidence_relationship_refs.v1",
		},
	})
	if err != nil {
		res.RecallID = ""
	}
}

func v2RecallFeedbackToolArgs(input map[string]any, req memoryservice.V2RecallRequest) map[string]any {
	effective := map[string]any{
		"contract_version": req.ContractVersion,
		"query":            req.Query,
		"limit":            req.Limit,
		"include_evidence": req.IncludeEvidence,
		"use_communities":  req.UseCommunities,
	}
	if req.ValidAt != nil {
		effective["valid_at"] = req.ValidAt.UTC().Format(v2FeedbackTimeFormat)
	}
	if req.KnownAt != nil {
		effective["known_at"] = req.KnownAt.UTC().Format(v2FeedbackTimeFormat)
	}
	if len(req.KnownEvidenceIDs) > 0 {
		effective["known_evidence_ids"] = append([]string(nil), req.KnownEvidenceIDs...)
	}
	if len(req.KnownRelationshipIDs) > 0 {
		effective["known_relationship_ids"] = append([]string(nil), req.KnownRelationshipIDs...)
	}
	if len(req.ExpandFromEntityIDs) > 0 {
		effective["expand_from_entity_ids"] = append([]string(nil), req.ExpandFromEntityIDs...)
	}
	return map[string]any{
		"input":     v2RecallFeedbackInputCopy(input),
		"effective": effective,
	}
}

const v2FeedbackTimeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func v2RecallFeedbackInputCopy(input map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"query",
		"limit",
		"valid_at",
		"known_at",
		"known_evidence_ids",
		"known_relationship_ids",
		"expand_from_entity_ids",
		"include_evidence",
		"use_communities",
	} {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	return out
}

func v2RecallFeedbackResultRefs(res *memoryservice.V2RecallResult) []domain.RecallFeedbackResultRef {
	if res == nil {
		return []domain.RecallFeedbackResultRef{}
	}
	refs := make([]domain.RecallFeedbackResultRef, 0, len(res.Results)*2)
	for _, item := range res.Results {
		rank := item.Rank
		if rank <= 0 {
			rank = len(refs) + 1
		}
		if item.EvidenceID != "" {
			refs = append(refs, domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeEvidence,
				ID:             item.EvidenceID,
				Rank:           rank,
				Tier:           "evidence",
				StatusAtRecall: res.SearchState,
			})
		}
		for _, relationshipID := range item.RelationshipIDs {
			if relationshipID == "" {
				continue
			}
			refs = append(refs, domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeRelationship,
				ID:             relationshipID,
				Rank:           rank,
				Tier:           "relationship",
				StatusAtRecall: res.SearchState,
			})
		}
	}
	return refs
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
