package registry

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
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
			"Submit exact evidence and optional relationship proposals for one synchronous atomic semantic commit. Each proposal cites its submitted evidence; safe evidence is stored even when no relationship is accepted. The server and assessor own exact grounding. Use exactly one object shape: {\"object\":{\"entity\":{\"name\":\"PostgreSQL\",\"entity_kind\":\"product\"}}} or {\"object\":{\"value\":{\"type\":\"string\",\"value\":\"PostgreSQL\"}}}.",
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
		tools[i] = bindContractTool(tools[i], deps)
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
		OutputSchema:   actionableOutputSchema(output),
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
	if err := validateSubmittedRelationships(args["relationships"], evidence, "relationships", true); err != nil {
		return err
	}
	return nil
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
		if err := validateUUIDStringArray(args, field); err != nil {
			return err
		}
	}
	return nil
}

func validateUUIDStringArray(args map[string]any, field string) error {
	raw, ok := args[field]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	for index, item := range items {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%s[%d] must be a valid UUID", field, index)
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
