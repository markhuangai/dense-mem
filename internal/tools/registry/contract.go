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
	ToolRemember                    = "remember"
	ToolRetractEvidence             = "retract_evidence"
	ToolCorrectRelationship         = "correct_relationship"
	ToolRecallMemory                = "recall_memory"
	ToolTraceMemory                 = "trace_memory"
	ToolSubmitRecallSessionFeedback = "submit_recall_session_feedback"
	ToolListDreams                  = "list_dreams"
	ToolGetDream                    = "get_dream"
	ToolResolveDreamFeedback        = "resolve_dream_feedback"
	ToolExportMemoryPack            = "export_memory_pack"
)

// Community analysis stays server-controlled recall context and first-party
// portal data; it is intentionally absent from the public MCP catalog.
var contractToolNames = []string{
	ToolRemember,
	ToolRetractEvidence,
	ToolCorrectRelationship,
	ToolRecallMemory,
	ToolTraceMemory,
	ToolSubmitRecallSessionFeedback,
	ToolListDreams,
	ToolGetDream,
	ToolResolveDreamFeedback,
	ToolExportMemoryPack,
}

// ContractTools returns the frozen contract tool contract catalog. The returned
// tools are metadata only until the memory feature gate wires real invokers.
func ContractTools() []Tool {
	return []Tool{
		contractTool(
			ToolRemember,
			"Submit exact evidence and relationship proposals for one synchronous atomic semantic commit; every submitted evidence item must be cited by evidence_index. The server and assessor own exact grounding. Use exactly one object shape: {\"object\":{\"entity\":{\"name\":\"PostgreSQL\",\"entity_kind\":\"product\"}}} or {\"object\":{\"value\":{\"type\":\"string\",\"value\":\"PostgreSQL\"}}}.",
			[]string{"write"},
			rememberInputSchema(),
			rememberOutputSchema(),
		),
		contractTool(
			ToolRetractEvidence,
			"Retract caller-owned evidence while preserving append-only provenance.",
			[]string{"write"},
			retractEvidenceInputSchema(),
			retractEvidenceOutputSchema(),
		),
		contractTool(
			ToolCorrectRelationship,
			"Replace one caller-owned Relationship while preserving its existing evidence provenance and superseding the old Relationship atomically.",
			[]string{"write"},
			correctRelationshipInputSchema(),
			correctRelationshipOutputSchema(),
		),
		contractTool(
			ToolRecallMemory,
			"Recall active evidence contexts and graph-shaped Relationship handles.",
			[]string{"read"},
			recallMemoryInputSchema(),
			recallMemoryOutputSchema(),
		),
		contractTool(
			ToolTraceMemory,
			"Trace one same-team Relationship through evidence, decisions, and lineage.",
			[]string{"read"},
			traceMemoryInputSchema(),
			traceMemoryOutputSchema(),
		),
		contractTool(
			ToolSubmitRecallSessionFeedback,
			"Record bounded session-level recall quality feedback.",
			[]string{"write"},
			recallFeedbackInputSchema(),
			recallFeedbackOutputSchema(),
		),
		contractTool(
			ToolListDreams,
			"List reviewable Hypotheses without treating them as memory.",
			[]string{"read"},
			listDreamsInputSchema(),
			listDreamsOutputSchema(),
		),
		contractTool(
			ToolGetDream,
			"Fetch one authorized Hypothesis and its source refs.",
			[]string{"read"},
			getDreamInputSchema(),
			getDreamOutputSchema(),
		),
		contractTool(
			ToolResolveDreamFeedback,
			"Resolve Hypothesis feedback without using the Hypothesis as evidence.",
			[]string{"write"},
			resolveDreamFeedbackInputSchema(),
			resolveDreamFeedbackOutputSchema(),
		),
		contractTool(
			ToolExportMemoryPack,
			"Export selected active Relationships with support provenance.",
			[]string{"read"},
			exportMemoryPackInputSchema(),
			exportMemoryPackOutputSchema(),
		),
	}
}

func contractTools(deps Dependencies) []Tool {
	tools := ContractTools()
	for i := range tools {
		tools[i].Visibility = "active"
		switch tools[i].Name {
		case ToolRemember:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Remember == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("remember: invalid input: %w", err)
				}
				req, err := rememberRequestFromContractInput(input)
				if err != nil {
					return nil, fmt.Errorf("remember: invalid input: %w", err)
				}
				res, err := deps.Remember.Remember(ctx, req)
				if err != nil {
					validation := wrapRememberValidationError(err)
					if _, ok := ContractValidationResultFromError(validation); ok {
						return nil, validation
					}
					return nil, rememberToolResultError(ctx, err)
				}
				result, err := structToMap(res)
				if err != nil {
					return nil, err
				}
				state := strings.TrimSpace(fmt.Sprint(result["processing_state"]))
				if state == "rejected" || state == "quarantined" {
					return nil, NewToolResultError(result)
				}
				return result, nil
			}
		case ToolRetractEvidence:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Lifecycle == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("retract_evidence: invalid input: %w", err)
				}
				var req memoryservice.RetractEvidenceRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("retract_evidence: invalid input: %w", err)
				}
				res, err := deps.Lifecycle.RetractEvidence(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		case ToolCorrectRelationship:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Lifecycle == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("correct_relationship: invalid input: %w", err)
				}
				var req memoryservice.CorrectRelationshipRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("correct_relationship: invalid input: %w", err)
				}
				res, err := deps.Lifecycle.CorrectRelationship(ctx, req)
				if err != nil {
					submissionID := ""
					if req.Action == "confirm" {
						submissionID = req.SubmissionID
					}
					return nil, correctionToolResultError(ctx, submissionID, err)
				}
				result, err := structToMap(res)
				if err != nil {
					return nil, err
				}
				if state := strings.TrimSpace(fmt.Sprint(result["processing_state"])); state == "rejected" {
					return nil, NewToolResultError(result)
				}
				return result, nil
			}
		case ToolRecallMemory:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Recall == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
				}
				var req memoryservice.RecallRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
				}
				dreamingEnabled := DreamingEnabled(ctx, deps.Dreams)
				req.IncludeHypotheses = dreamingEnabled
				res, err := deps.Recall.Recall(ctx, req)
				if err != nil {
					return nil, err
				}
				if res != nil && !dreamingEnabled {
					res.RelatedHypotheses = []memoryservice.RelatedHypothesisSummary{}
				}
				feedbackSnapshotStored := recordRecallFeedbackSnapshot(ctx, deps, input, req, res)
				setRecallSuggestedActions(res, feedbackSnapshotStored, dreamingEnabled)
				return recallContractOutput(res), nil
			}
		case ToolTraceMemory:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.Context == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
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
					return traceContractOutput(res.Semantic)
				}
				return structToMap(res)
			}
		case ToolSubmitRecallSessionFeedback:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.RecallFeedbackEvents == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("submit_recall_session_feedback: invalid input: %w", err)
				}
				return submitRecallFeedback(ctx, deps, input)
			}
		case ToolListDreams:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("list_dreams: invalid input: %w", err)
				}
				opts := dreamservice.ListOptions{
					Status: stringInput(input["status"]),
					Cursor: stringInput(input["cursor"]),
				}
				if limit, ok := intInput(input["limit"]); ok {
					opts.Limit = limit
				}
				dreams, next, err := deps.Dreams.List(ctx, teamID, opts)
				if err != nil {
					return nil, err
				}
				return listDreamsContractOutput(dreams, next), nil
			}
		case ToolGetDream:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("get_dream: invalid input: %w", err)
				}
				dream, err := deps.Dreams.Get(ctx, teamID, stringInput(input["hypothesis_id"]))
				if err != nil {
					return nil, err
				}
				return map[string]any{"hypothesis": dreamContractOutput(dream)}, nil
			}
		case ToolResolveDreamFeedback:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
				if deps.Dreams == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
				}
				req, err := resolveDreamFeedbackRequestFromContractInput(input)
				if err != nil {
					return nil, fmt.Errorf("resolve_dream_feedback: invalid input: %w", err)
				}
				res, err := deps.Dreams.ResolveFeedback(ctx, teamID, req)
				if err != nil {
					return nil, err
				}
				return resolveDreamFeedbackContractOutput(res), nil
			}
		case ToolExportMemoryPack:
			tool := tools[i]
			tools[i].Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
				if deps.MemoryPack == nil {
					return nil, ErrToolUnavailable
				}
				if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
					return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
				}
				var req skillpackservice.ExportRequest
				if err := remapInput(input, &req); err != nil {
					return nil, fmt.Errorf("export_memory_pack: invalid input: %w", err)
				}
				res, err := deps.MemoryPack.Export(ctx, req)
				if err != nil {
					return nil, err
				}
				return exportMemoryPackContractOutput(res), nil
			}
		}
	}
	return tools
}

func objectArray(value any) []map[string]any {
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

func contractTool(
	name string,
	description string,
	scopes []string,
	input map[string]any,
	output map[string]any,
) Tool {
	return Tool{
		Name:           name,
		Description:    description,
		InputSchema:    input,
		OutputSchema:   output,
		RequiredScopes: scopes,
		FeatureGate:    domain.FeatureGate,
		Visibility:     domain.ToolVisibility,
	}
}

func ContractToolNames() []string {
	return append([]string(nil), contractToolNames...)
}

func IsContractTool(tool Tool) bool {
	if tool.FeatureGate != domain.FeatureGate {
		return false
	}
	for _, name := range contractToolNames {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func ValidateContractInput(tool Tool, args map[string]any, scopes []string) error {
	if !ToolScopesSatisfied(tool, scopes) {
		return fmt.Errorf("%s: missing required scope", tool.Name)
	}
	if HasTenantOverrideArgs(args) {
		return fmt.Errorf("%s: team_id and profile_id are not accepted", tool.Name)
	}
	if tool.Name == ToolRemember {
		if _, ok := args["evidence"]; !ok {
			return fmt.Errorf("evidence is required")
		}
		if _, ok := args["relationships"]; !ok {
			return fmt.Errorf("relationships is required")
		}
	}
	if err := ValidateInput(tool, args); err != nil {
		return err
	}
	switch tool.Name {
	case ToolRemember:
		return validateRemember(args)
	case ToolRecallMemory:
		return validateRecall(args)
	case ToolRetractEvidence:
		return validateUniqueStringArray(args, "evidence_ids")
	case ToolTraceMemory:
		return validateUniqueStringArray(args, "predicate_keys")
	case ToolCorrectRelationship:
		return validateCorrectRelationship(args)
	case ToolSubmitRecallSessionFeedback:
		return validateRecallFeedback(args)
	case ToolResolveDreamFeedback:
		return validateDreamFeedback(args)
	case ToolExportMemoryPack:
		return validateUniqueStringArray(args, "relationship_ids")
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

func validateRemember(args map[string]any) error {
	evidence, _ := args["evidence"].([]any)
	sourceRevisions := map[string]contractSourceRevision{}
	for i, item := range evidence {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		if err := validateSourceRevisionFields(i, fields); err != nil {
			return err
		}
		if err := validateSourceRevisionBatch(i, fields, sourceRevisions); err != nil {
			return err
		}
	}
	if err := validateDirectEvidenceSupersessions(evidence); err != nil {
		return err
	}
	if err := validateSubmittedRelationships(args["relationships"], evidence, "relationships"); err != nil {
		return err
	}
	return validateSubmittedEvidenceCoverage(args["relationships"], evidence, "relationships")
}

func validateRecall(args map[string]any) error {
	for _, field := range []string{
		"known_evidence_ids",
		"known_relationship_ids",
		"expand_from_entity_ids",
	} {
		if err := validateUniqueStringArray(args, field); err != nil {
			return err
		}
	}
	return nil
}

func validateSourceRevisionFields(index int, fields map[string]any) error {
	_, hasSourceKey := fields["source_key"]
	_, hasSourceRevision := fields["source_revision"]
	_, hasPreviousRevision := fields["previous_source_revision"]
	_, hasSupersededEvidence := fields["supersedes_evidence_ids"]
	if hasSourceKey != hasSourceRevision {
		missing := "source_revision"
		if !hasSourceKey {
			missing = "source_key"
		}
		return fmt.Errorf("evidence[%d].%s: source_key and source_revision must appear together", index, missing)
	}
	if hasPreviousRevision && (!hasSourceKey || !hasSourceRevision) {
		return fmt.Errorf(
			"evidence[%d].previous_source_revision: requires source_key and source_revision",
			index,
		)
	}
	if hasSupersededEvidence && hasPreviousRevision {
		return fmt.Errorf(
			"evidence[%d].supersedes_evidence_ids: cannot be combined with previous_source_revision",
			index,
		)
	}
	return nil
}

func validateRequiredFields(args map[string]any, fields ...string) error {
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

func validateUniqueStringArray(args map[string]any, field string) error {
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
			return fmt.Errorf("%s[%d]: duplicate value", field, i)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func requireTool(tools map[string]Tool, name string) (Tool, error) {
	tool, ok := tools[name]
	if !ok {
		return Tool{}, fmt.Errorf("missing contract tool %s", name)
	}
	if tool.FeatureGate != domain.FeatureGate || tool.Visibility != domain.ToolVisibility {
		return Tool{}, fmt.Errorf("tool %s has wrong gate metadata", name)
	}
	return tool, nil
}
