package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

const (
	V2ToolRemember                    = "remember"
	V2ToolGetMemoryPlacement          = "get_memory_placement"
	V2ToolResolveMemoryPlacement      = "resolve_memory_placement"
	V2ToolCorrectEntityResolution     = "correct_entity_resolution"
	V2ToolRecallMemory                = "recall_memory"
	V2ToolTraceMemory                 = "trace_memory"
	V2ToolSubmitRecallSessionFeedback = "submit_recall_session_feedback"
	V2ToolListDreams                  = "list_dreams"
	V2ToolGetDream                    = "get_dream"
	V2ToolResolveDreamFeedback        = "resolve_dream_feedback"
	V2ToolFindMemoryPackCandidates    = "find_memory_pack_candidates"
	V2ToolExportMemoryPack            = "export_memory_pack"
	V2ToolInspectMemoryPack           = "inspect_memory_pack"
	V2ToolImportMemoryPack            = "import_memory_pack"
	V2ToolRollbackMemoryPackImport    = "rollback_memory_pack_import"
)

// Community analysis stays server-controlled recall context and first-party
// portal data; it is intentionally absent from the public MCP catalog.
var v2ContractToolNames = []string{
	V2ToolRemember,
	V2ToolGetMemoryPlacement,
	V2ToolResolveMemoryPlacement,
	V2ToolCorrectEntityResolution,
	V2ToolRecallMemory,
	V2ToolTraceMemory,
	V2ToolSubmitRecallSessionFeedback,
	V2ToolListDreams,
	V2ToolGetDream,
	V2ToolResolveDreamFeedback,
	V2ToolFindMemoryPackCandidates,
	V2ToolExportMemoryPack,
	V2ToolInspectMemoryPack,
	V2ToolImportMemoryPack,
	V2ToolRollbackMemoryPackImport,
}

// V2ContractTools returns the frozen V2 tool contract catalog. The returned
// tools are metadata only until the V2 feature gate wires real invokers.
func V2ContractTools() []Tool {
	return []Tool{
		v2ContractTool(
			V2ToolRemember,
			"Submit exact evidence and optional proposal hints for server-owned placement.",
			[]string{"write"},
			v2RememberInputSchema(),
			v2RememberOutputSchema(),
		),
		v2ContractTool(
			V2ToolGetMemoryPlacement,
			"Poll a V2 memory placement run.",
			[]string{"read"},
			v2GetMemoryPlacementInputSchema(),
			v2PlacementRunOutputSchema(),
		),
		v2ContractTool(
			V2ToolResolveMemoryPlacement,
			"Resolve V2 placement review items through append-only evidence decisions.",
			[]string{"write"},
			v2ResolveMemoryPlacementInputSchema(),
			v2ResolveMemoryPlacementOutputSchema(),
		),
		v2ContractTool(
			V2ToolCorrectEntityResolution,
			"Dry-run or apply caller-owned Entity merge and split corrections.",
			[]string{"write"},
			v2CorrectEntityResolutionInputSchema(),
			v2CorrectEntityResolutionOutputSchema(),
		),
		v2ContractTool(
			V2ToolRecallMemory,
			"Recall active evidence contexts and graph-shaped Relationship handles.",
			[]string{"read"},
			v2RecallMemoryInputSchema(),
			v2RecallMemoryOutputSchema(),
		),
		v2ContractTool(
			V2ToolTraceMemory,
			"Trace one same-team Relationship through evidence, decisions, and lineage.",
			[]string{"read"},
			v2TraceMemoryInputSchema(),
			v2TraceMemoryOutputSchema(),
		),
		v2ContractTool(
			V2ToolSubmitRecallSessionFeedback,
			"Record bounded session-level recall quality feedback.",
			[]string{"write"},
			v2RecallFeedbackInputSchema(),
			v2RecallFeedbackOutputSchema(),
		),
		v2ContractTool(
			V2ToolListDreams,
			"List reviewable Hypotheses without treating them as memory.",
			[]string{"read"},
			v2ListDreamsInputSchema(),
			v2ListDreamsOutputSchema(),
		),
		v2ContractTool(
			V2ToolGetDream,
			"Fetch one authorized Hypothesis and its source refs.",
			[]string{"read"},
			v2GetDreamInputSchema(),
			v2GetDreamOutputSchema(),
		),
		v2ContractTool(
			V2ToolResolveDreamFeedback,
			"Resolve Hypothesis feedback without using the Hypothesis as evidence.",
			[]string{"write"},
			v2ResolveDreamFeedbackInputSchema(),
			v2ResolveDreamFeedbackOutputSchema(),
		),
		v2ContractTool(
			V2ToolFindMemoryPackCandidates,
			"Find active Relationships that may be exported into a memory pack.",
			[]string{"read"},
			v2FindMemoryPackCandidatesInputSchema(),
			v2FindMemoryPackCandidatesOutputSchema(),
		),
		v2ContractTool(
			V2ToolExportMemoryPack,
			"Export selected active Relationships with support provenance.",
			[]string{"read"},
			v2ExportMemoryPackInputSchema(),
			v2ExportMemoryPackOutputSchema(),
		),
		v2ContractTool(
			V2ToolInspectMemoryPack,
			"Inspect a memory-pack artifact without writing durable state.",
			[]string{"read"},
			v2InspectMemoryPackInputSchema(),
			v2InspectMemoryPackOutputSchema(),
		),
		v2ContractTool(
			V2ToolImportMemoryPack,
			"Import a reviewed memory pack through normal evidence placement.",
			[]string{"write"},
			v2ImportMemoryPackInputSchema(),
			v2ImportMemoryPackOutputSchema(),
		),
		v2ContractTool(
			V2ToolRollbackMemoryPackImport,
			"Rollback a V2 memory-pack import when no selected state changed.",
			[]string{"write"},
			v2RollbackMemoryPackImportInputSchema(),
			v2RollbackMemoryPackImportOutputSchema(),
		),
	}
}

func contractTools(deps Dependencies) []Tool {
	tools := V2ContractTools()
	for i := range tools {
		tools[i].Visibility = "active"
		switch tools[i].Name {
		case V2ToolRemember:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Remember == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("remember: invalid input: %w", err)
				}
				req, err := v2RememberRequestFromContractInput(input)
				if err != nil {
					return nil, fmt.Errorf("remember: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.Remember.Remember(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		case V2ToolGetMemoryPlacement:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Remember == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("get_memory_placement: invalid input: %w", err)
				}
				var req memoryservice.V2GetMemoryPlacementRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("get_memory_placement: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.Remember.GetMemoryPlacement(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		case V2ToolResolveMemoryPlacement:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Lifecycle == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("resolve_memory_placement: invalid input: %w", err)
				}
				var req memoryservice.V2ResolveMemoryPlacementRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("resolve_memory_placement: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.Lifecycle.ResolveMemoryPlacement(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		case V2ToolCorrectEntityResolution:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Lifecycle == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("correct_entity_resolution: invalid input: %w", err)
				}
				var req memoryservice.V2CorrectEntityResolutionRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("correct_entity_resolution: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.Lifecycle.CorrectEntityResolution(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		case V2ToolRecallMemory:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Recall == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
				}
				var req memoryservice.V2RecallRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
				}
				req.ContractVersion = domain.V2ContractVersion
				res, err := deps.Recall.Recall(ctx, req)
				if err != nil {
					return nil, err
				}
				recordV2RecallFeedbackSnapshot(ctx, deps, input, req, res)
				return v2RecallContractOutput(res), nil
			}
		case V2ToolTraceMemory:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Context == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
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
				if res != nil && res.Semantic != nil {
					return v2TraceContractOutput(res.Semantic)
				}
				return structToMap(res)
			}
		case V2ToolListDreams:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("list_dreams: invalid input: %w", err)
				}
				opts := dreamservice.ListOptions{
					Status: stringInput(input["status"]),
					Cursor: stringInput(input["cursor"]),
				}
				if limit, ok := intInput(input["limit"]); ok {
					opts.Limit = limit
				}
				dreams, next, err := deps.Dreams.List(ctx, profileID, opts)
				if err != nil {
					return nil, err
				}
				return v2ListDreamsContractOutput(dreams, next), nil
			}
		case V2ToolGetDream:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("get_dream: invalid input: %w", err)
				}
				dream, err := deps.Dreams.Get(ctx, profileID, stringInput(input["hypothesis_id"]))
				if err != nil {
					return nil, err
				}
				return map[string]any{"hypothesis": v2DreamContractOutput(dream)}, nil
			}
		case V2ToolResolveDreamFeedback:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
				}
				req, err := v2ResolveDreamFeedbackRequestFromContractInput(input)
				if err != nil {
					return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
				}
				res, err := deps.Dreams.ResolveFeedback(ctx, profileID, req)
				if err != nil {
					return nil, err
				}
				return v2ResolveDreamFeedbackContractOutput(res), nil
			}
		case V2ToolFindMemoryPackCandidates:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.MemoryPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("find_memory_pack_candidates: invalid input: %w", err)
				}
				var req skillpackservice.V2FindCandidatesRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("find_memory_pack_candidates: invalid input: %w", err)
				}
				res, err := deps.MemoryPack.FindCandidates(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2FindMemoryPackCandidatesContractOutput(res), nil
			}
		case V2ToolExportMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.MemoryPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.V2ExportRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
				}
				res, err := deps.MemoryPack.Export(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2ExportMemoryPackContractOutput(res), nil
			}
		case V2ToolInspectMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.MemoryPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("inspect_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.V2InspectRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("inspect_memory_pack: invalid input: %w", err)
				}
				res, err := deps.MemoryPack.Inspect(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2InspectMemoryPackContractOutput(res, req.Mode), nil
			}
		case V2ToolImportMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.MemoryPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("import_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.V2ImportRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("import_memory_pack: invalid input: %w", err)
				}
				res, err := deps.MemoryPack.Import(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2ImportMemoryPackContractOutput(res), nil
			}
		case V2ToolRollbackMemoryPackImport:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.MemoryPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateV2ContractInput(tool, input, v2AuthenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("rollback_memory_pack_import: invalid input: %w", err)
				}
				var req skillpackservice.V2RollbackRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("rollback_memory_pack_import: invalid input: %w", err)
				}
				if !req.DryRun {
					req.Confirm = true
				}
				res, err := deps.MemoryPack.Rollback(ctx, req)
				if err != nil {
					return nil, err
				}
				return v2RollbackMemoryPackContractOutput(res), nil
			}
		}
	}
	return tools
}

func v2ObjectArray(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		fields, ok := objectFields(item)
		if ok {
			out = append(out, fields)
		}
	}
	return out
}

func v2ContractTool(
	name string,
	description string,
	scopes []string,
	input map[string]any,
	output map[string]any,
) Tool {
	return Tool{
		Name:            name,
		Description:     description,
		InputSchema:     input,
		OutputSchema:    output,
		RequiredScopes:  scopes,
		ContractVersion: domain.V2ContractVersion,
		FeatureGate:     domain.V2FeatureGate,
		Visibility:      domain.V2ToolVisibility,
	}
}

func V2ContractToolNames() []string {
	return append([]string(nil), v2ContractToolNames...)
}

func IsV2ContractTool(tool Tool) bool {
	if tool.FeatureGate != domain.V2FeatureGate {
		return false
	}
	for _, name := range v2ContractToolNames {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func ValidateV2ContractInput(tool Tool, args map[string]any, scopes []string) error {
	if tool.ContractVersion != domain.V2ContractVersion {
		return fmt.Errorf(
			"%s: tool contract version %q is not %q",
			tool.Name,
			tool.ContractVersion,
			domain.V2ContractVersion,
		)
	}
	if !ToolScopesSatisfied(tool, scopes) {
		return fmt.Errorf("%s: missing required scope", tool.Name)
	}
	if HasTenantOverrideArgs(args) {
		return fmt.Errorf("%s: team_id and profile_id are not accepted", tool.Name)
	}
	if err := ValidateInput(tool, args); err != nil {
		return err
	}
	switch tool.Name {
	case V2ToolRemember:
		return validateV2Remember(args)
	case V2ToolRecallMemory:
		return validateV2Recall(args)
	case V2ToolTraceMemory:
		return validateV2UniqueStringArray(args, "predicate_keys")
	case V2ToolResolveMemoryPlacement:
		return validateV2ResolveMemoryPlacement(args)
	case V2ToolCorrectEntityResolution:
		return validateV2CorrectEntityResolution(args)
	case V2ToolSubmitRecallSessionFeedback:
		return validateV2RecallFeedback(args)
	case V2ToolResolveDreamFeedback:
		return validateV2DreamFeedback(args)
	case V2ToolFindMemoryPackCandidates:
		return validateV2UniqueStringArray(args, "predicate_keys")
	case V2ToolExportMemoryPack:
		return validateV2UniqueStringArray(args, "relationship_ids")
	case V2ToolInspectMemoryPack, V2ToolImportMemoryPack:
		return validateV2MemoryPackSource(args)
	case V2ToolRollbackMemoryPackImport:
		return validateV2RollbackMemoryPackImport(args)
	default:
		return nil
	}
}

func ToolScopesSatisfied(tool Tool, scopes []string) bool {
	if len(tool.RequiredScopes) == 0 {
		return true
	}
	scopeSet := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scopeSet[scope] = struct{}{}
	}
	for _, required := range tool.RequiredScopes {
		if _, ok := scopeSet[required]; !ok {
			return false
		}
	}
	return true
}

func validateV2Remember(args map[string]any) error {
	evidence, _ := args["evidence"].([]any)
	sourceRevisions := map[string]v2ContractSourceRevision{}
	for i, item := range evidence {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		if err := validateV2SourceRevisionFields(i, fields); err != nil {
			return err
		}
		if err := validateV2SourceRevisionBatch(i, fields, sourceRevisions); err != nil {
			return err
		}
	}
	proposal, _ := objectFields(args["proposal"])
	if err := validateV2UniqueObjectRefsIn(proposal["entities"], "proposal.entities", "ref"); err != nil {
		return err
	}
	if err := validateV2UniqueObjectRefsIn(proposal["relationships"], "proposal.relationships", "proposal_id"); err != nil {
		return err
	}
	if err := validateV2RelationshipObjectChoiceIn(proposal["relationships"], "proposal.relationships"); err != nil {
		return err
	}
	return validateV2ProposalReferencesAndSpans(proposal, evidence)
}

func validateV2Recall(args map[string]any) error {
	for _, field := range []string{
		"known_evidence_ids",
		"known_relationship_ids",
		"expand_from_entity_ids",
	} {
		if err := validateV2UniqueStringArray(args, field); err != nil {
			return err
		}
	}
	return nil
}

func validateV2ResolveMemoryPlacement(args map[string]any) error {
	action, _ := args["action"].(string)
	// Item-targeted decisions require the inspected version so stale reviews fail closed.
	switch domain.V2ResolveAction(action) {
	case domain.V2ResolveAcknowledge:
		return validateV2RequiredFields(args, "ingest_id")
	case domain.V2ResolveSelectEntity:
		return validateV2RequiredFields(args,
			"ingest_id",
			"placement_item_id",
			"placement_item_version",
			"entity_ref",
			"candidate_entity_id",
		)
	case domain.V2ResolveConfirmNewEntity:
		return validateV2RequiredFields(args,
			"ingest_id",
			"placement_item_id",
			"placement_item_version",
			"entity_ref",
			"evidence",
		)
	case domain.V2ResolveSelectPredicate:
		return validateV2RequiredFields(args,
			"ingest_id",
			"placement_item_id",
			"placement_item_version",
			"observation_id",
			"predicate_key",
			"predicate_version",
		)
	case domain.V2ResolveAccept:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "placement_item_version", "evidence")
	case domain.V2ResolveReject:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "placement_item_version", "message")
	case domain.V2ResolveCorrect:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "placement_item_version", "evidence")
	case domain.V2ResolveReleaseQuarantine:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "placement_item_version", "message")
	case domain.V2ResolveForget:
		return validateV2RequiredFields(args, "relationship_id", "message", "evidence")
	default:
		return nil
	}
}

func validateV2SourceRevisionFields(index int, fields map[string]any) error {
	_, hasSourceKey := fields["source_key"]
	_, hasSourceRevision := fields["source_revision"]
	_, hasPreviousRevision := fields["previous_source_revision"]
	_, hasSupersededFragments := fields["supersedes_fragment_ids"]
	if hasSourceKey != hasSourceRevision {
		return fmt.Errorf("evidence[%d]: source_key and source_revision must appear together", index)
	}
	if hasPreviousRevision && (!hasSourceKey || !hasSourceRevision) {
		return fmt.Errorf(
			"evidence[%d]: previous_source_revision requires source_key and source_revision",
			index,
		)
	}
	if hasSupersededFragments && (!hasSourceKey || !hasSourceRevision) {
		return fmt.Errorf(
			"evidence[%d]: supersedes_fragment_ids requires source_key and source_revision",
			index,
		)
	}
	return nil
}

func validateV2RequiredFields(args map[string]any, fields ...string) error {
	for _, field := range fields {
		value, ok := args[field]
		if !ok {
			return fmt.Errorf("%s is required for action %s", field, args["action"])
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is required for action %s", field, args["action"])
		}
	}
	return nil
}

func validateV2UniqueStringArray(args map[string]any, field string) error {
	raw, ok := args[field]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	for i, item := range items {
		value, ok := item.(string)
		if !ok || value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s[%d]: duplicate value %q", field, i, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func requireV2Tool(tools map[string]Tool, name string) (Tool, error) {
	tool, ok := tools[name]
	if !ok {
		return Tool{}, fmt.Errorf("missing V2 tool %s", name)
	}
	if tool.ContractVersion != domain.V2ContractVersion {
		return Tool{}, fmt.Errorf("tool %s has wrong contract version", name)
	}
	if tool.FeatureGate != domain.V2FeatureGate || tool.Visibility != domain.V2ToolVisibility {
		return Tool{}, fmt.Errorf("tool %s has wrong V2 gate metadata", name)
	}
	return tool, nil
}
