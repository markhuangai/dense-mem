package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
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
	V2ToolListCommunities             = "list_communities"
	V2ToolFindMemoryPackCandidates    = "find_memory_pack_candidates"
	V2ToolExportMemoryPack            = "export_memory_pack"
	V2ToolInspectMemoryPack           = "inspect_memory_pack"
	V2ToolImportMemoryPack            = "import_memory_pack"
	V2ToolRollbackMemoryPackImport    = "rollback_memory_pack_import"
)

// V2ContractTools returns the frozen V2 tool contract catalog. The returned
// tools are metadata only until the V2 feature gate wires real invokers.
func V2ContractTools() []Tool {
	return []Tool{
		v2ContractTool(
			V2ToolRemember,
			"Submit exact evidence and optional proposal hints for server-owned placement.",
			[]string{"write"},
			v2RememberInputSchema(),
			v2PlacementRunOutputSchema(),
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
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolListDreams,
			"List reviewable Hypotheses without treating them as memory.",
			[]string{"read"},
			v2ListDreamsInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolGetDream,
			"Fetch one authorized Hypothesis and its source refs.",
			[]string{"read"},
			v2GetDreamInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolResolveDreamFeedback,
			"Resolve Hypothesis feedback without using the Hypothesis as evidence.",
			[]string{"write"},
			v2ResolveDreamFeedbackInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolListCommunities,
			"List bounded same-team community summaries as derived read-model data.",
			[]string{"read"},
			v2ListCommunitiesInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolFindMemoryPackCandidates,
			"Find active Relationships that may be exported into a memory pack.",
			[]string{"read"},
			v2FindMemoryPackCandidatesInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolExportMemoryPack,
			"Export selected active Relationships with support provenance.",
			[]string{"read"},
			v2ExportMemoryPackInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolInspectMemoryPack,
			"Inspect a memory-pack artifact without writing durable state.",
			[]string{"read"},
			v2InspectMemoryPackInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolImportMemoryPack,
			"Import a reviewed memory pack through normal evidence placement.",
			[]string{"write"},
			v2ImportMemoryPackInputSchema(),
			map[string]any{"type": "object"},
		),
		v2ContractTool(
			V2ToolRollbackMemoryPackImport,
			"Rollback a V2 memory-pack import when no selected state changed.",
			[]string{"write"},
			v2RollbackMemoryPackImportInputSchema(),
			map[string]any{"type": "object"},
		),
	}
}

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
				res, err := deps.V2Remember.RememberV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
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
				res, err := deps.V2Recall.RecallV2(ctx, req)
				if err != nil {
					return nil, err
				}
				return structToMap(res)
			}
		}
	}
	return tools
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
	tools := V2ContractTools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func IsV2ContractTool(tool Tool) bool {
	return tool.ContractVersion == domain.V2ContractVersion
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
	case V2ToolResolveMemoryPlacement:
		return validateV2ResolveMemoryPlacement(args)
	case V2ToolCorrectEntityResolution:
		return validateV2UniqueStringArray(args, "selected_observation_ids")
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
	sourceRevisions := make(map[string]v2ContractSourceRevision)
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
	if err := validateV2UniqueObjectRefs(args, "entity_hints", "ref"); err != nil {
		return err
	}
	if err := validateV2UniqueObjectRefs(args, "relationship_hints", "ref"); err != nil {
		return err
	}
	return validateV2RelationshipObjectChoice(args, "relationship_hints")
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
	switch domain.V2ResolveAction(action) {
	case domain.V2ResolveAcknowledge:
		return validateV2RequiredFields(args, "ingest_id")
	case domain.V2ResolveSelectEntity:
		return validateV2RequiredFields(args,
			"ingest_id",
			"placement_item_id",
			"entity_ref",
			"candidate_entity_id",
		)
	case domain.V2ResolveConfirmNewEntity:
		return validateV2RequiredFields(args,
			"ingest_id",
			"placement_item_id",
			"entity_ref",
			"evidence",
		)
	case domain.V2ResolveSelectPredicate:
		return validateV2RequiredFields(args,
			"ingest_id",
			"placement_item_id",
			"observation_id",
			"predicate_key",
			"predicate_version",
		)
	case domain.V2ResolveAccept:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "evidence")
	case domain.V2ResolveReject:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "message")
	case domain.V2ResolveCorrect:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "evidence")
	case domain.V2ResolveReleaseQuarantine:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "message")
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
	if hasSourceKey != hasSourceRevision {
		return fmt.Errorf("evidence[%d]: source_key and source_revision must appear together", index)
	}
	if hasPreviousRevision && (!hasSourceKey || !hasSourceRevision) {
		return fmt.Errorf(
			"evidence[%d]: previous_source_revision requires source_key and source_revision",
			index,
		)
	}
	return nil
}

type v2ContractSourceRevision struct {
	Revision string
	Previous string
}

func validateV2SourceRevisionBatch(index int, fields map[string]any, seen map[string]v2ContractSourceRevision) error {
	sourceKey, _ := fields["source_key"].(string)
	if sourceKey == "" {
		return nil
	}
	current := v2ContractSourceRevision{
		Revision: v2ContractStringField(fields, "source_revision"),
		Previous: v2ContractStringField(fields, "previous_source_revision"),
	}
	previous, ok := seen[sourceKey]
	if !ok {
		seen[sourceKey] = current
		return nil
	}
	if previous != current {
		return fmt.Errorf("evidence[%d]: source_key %q revision fields must match earlier item in request", index, sourceKey)
	}
	return nil
}

func v2ContractStringField(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
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

func validateV2UniqueObjectRefs(args map[string]any, field string, refField string) error {
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
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		value, _ := fields[refField].(string)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s[%d].%s: duplicate ref %q", field, i, refField, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateV2RelationshipObjectChoice(args map[string]any, field string) error {
	raw, ok := args[field]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	for i, item := range items {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		objectRef, hasObjectRef := fields["object_ref"].(string)
		hasObjectRef = hasObjectRef && strings.TrimSpace(objectRef) != ""
		_, hasObjectValue := fields["object_value"]
		if hasObjectRef == hasObjectValue {
			return fmt.Errorf("%s[%d]: exactly one of object_ref or object_value is required", field, i)
		}
	}
	return nil
}

func v2ContractInput(required []string, properties map[string]any) map[string]any {
	props := map[string]any{
		"contract_version": schemaEnum([]string{domain.V2ContractVersion}),
	}
	for key, value := range properties {
		props[key] = value
	}
	req := append([]string{"contract_version"}, required...)
	return map[string]any{
		"type":                 "object",
		"required":             req,
		"properties":           props,
		"additionalProperties": false,
	}
}

func v2RememberInputSchema() map[string]any {
	return v2ContractInput([]string{"evidence"}, map[string]any{
		"evidence":           v2EvidenceArraySchema(),
		"entity_hints":       v2EntityHintArraySchema(),
		"relationship_hints": v2RelationshipHintArraySchema(),
		"idempotency_key":    schemaString("Operation retry key scoped to team and profile.", 128),
	})
}

func v2EvidenceArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 20,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"content"},
			"properties": map[string]any{
				"content":                  memoryEntryString("Verbatim evidence text."),
				"source_type":              schemaEnum([]string{"conversation", "document", "observation", "manual"}),
				"source":                   schemaString("Bounded provenance label.", 256),
				"authority":                schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
				"source_key":               schemaString("Stable source-owner-scoped identity.", 256),
				"source_revision":          schemaString("Opaque current source revision token.", 256),
				"previous_source_revision": schemaString("Exact previous source revision token.", 256),
				"supersedes_evidence_ids":  v2StringArraySchema("Evidence UUID to supersede.", 50, 128),
				"idempotency_key":          schemaString("Evidence retry key scoped to team and profile.", 128),
				"labels":                   v2StringArraySchema("Evidence label.", 20, 64),
				"metadata":                 map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": false,
		},
	}
}

func v2EntityHintArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 100,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"ref", "name"},
			"properties": map[string]any{
				"ref":              schemaString("Client-local Entity proposal ref.", 128),
				"name":             schemaString("Evidence-supported name.", 256),
				"kind":             schemaEnum(domain.V2EntityKinds()),
				"aliases":          v2StringArraySchema("Evidence-supported alias.", 20, 256),
				"identity_context": map[string]any{"type": "object", "additionalProperties": true},
			},
			"additionalProperties": false,
		},
	}
}

func v2RelationshipHintArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 200,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"ref", "subject_ref", "predicate", "evidence"},
			"oneOf": []any{
				map[string]any{
					"required": []string{"object_ref"},
					"not":      map[string]any{"required": []string{"object_value"}},
				},
				map[string]any{
					"required": []string{"object_value"},
					"not":      map[string]any{"required": []string{"object_ref"}},
				},
			},
			"properties": map[string]any{
				"ref":         schemaString("Client-local Relationship proposal ref.", 128),
				"subject_ref": schemaString("Entity proposal ref.", 128),
				"predicate":   schemaString("Registered predicate name.", 128),
				"object_ref":  schemaString("Entity proposal ref.", 128),
				"object_value": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"type":  schemaEnum(domain.V2ValueTypes()),
						"value": schemaString("Canonical typed value.", 1024),
					},
					"required": []string{"type", "value"},
				},
				"polarity": schemaEnum([]string{"+", "-"}),
				"evidence": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": 20,
					"items": map[string]any{
						"type":                 "object",
						"required":             []string{"evidence_index", "quote"},
						"additionalProperties": false,
						"properties": map[string]any{
							"evidence_index": map[string]any{"type": "integer", "minimum": 0},
							"quote":          schemaString("Exact source span.", 999),
							"span_start":     map[string]any{"type": "integer", "minimum": 0},
							"span_end":       map[string]any{"type": "integer", "minimum": 0},
						},
					},
				},
			},
			"additionalProperties": false,
		},
	}
}

func v2GetMemoryPlacementInputSchema() map[string]any {
	return v2ContractInput([]string{"ingest_id"}, map[string]any{
		"ingest_id": schemaString("Placement run ID returned by remember.", 128),
	})
}

func v2ResolveMemoryPlacementInputSchema() map[string]any {
	return v2ContractInput([]string{"action", "idempotency_key"}, map[string]any{
		"action":            schemaEnum(domain.V2ResolveActions()),
		"ingest_id":         schemaString("Placement run ID.", 128),
		"placement_item_id": schemaString("Review item ID.", 128),
		"observation_id":    schemaString("Unresolved observation ID.", 128),
		"relationship_id":   schemaString("Caller-owned Relationship ID.", 128),
		"entity_ref":        schemaString("Client-local Entity proposal ref.", 128),
		"candidate_entity_id": schemaString(
			"Server-supplied candidate Entity ID selected for this profile.",
			128,
		),
		"predicate_key": schemaString("Server-supplied registered predicate key.", 128),
		"predicate_version": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "Server-supplied predicate version selected for this observation.",
		},
		"message":         schemaString("Bounded reason or reviewer explanation.", 1000),
		"evidence":        v2EvidenceArraySchema(),
		"idempotency_key": schemaString("Resolution retry key scoped to team and profile.", 128),
	})
}

func v2CorrectEntityResolutionInputSchema() map[string]any {
	return v2ContractInput([]string{"action", "source_entity_id", "dry_run"}, map[string]any{
		"action":                   schemaEnum(domain.V2EntityCorrectionActions()),
		"source_entity_id":         schemaString("Caller-visible source Entity ID.", 128),
		"target_entity_id":         schemaString("Merge target Entity ID. Null for split.", 128),
		"selected_observation_ids": v2StringArraySchema("Caller-owned observation ID.", 200, 128),
		"dry_run":                  map[string]any{"type": "boolean"},
		"plan_token":               schemaString("Dry-run token required for apply.", 256),
		"evidence":                 v2EvidenceArraySchema(),
		"idempotency_key":          schemaString("Correction retry key scoped to team and profile.", 128),
	})
}

func v2RecallMemoryInputSchema() map[string]any {
	return v2ContractInput([]string{"query"}, map[string]any{
		"query":                  schemaString("Natural-language recall query.", 512),
		"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"valid_at":               map[string]any{"type": "string", "format": "date-time"},
		"known_at":               map[string]any{"type": "string", "format": "date-time"},
		"known_evidence_ids":     v2StringArraySchema("Evidence UUID already seen.", 200, 128),
		"known_relationship_ids": v2StringArraySchema("Relationship UUID already seen.", 200, 128),
		"expand_from_entity_ids": v2StringArraySchema("Entity UUID for focused expansion.", 50, 128),
		"include_evidence":       map[string]any{"type": "boolean"},
		"use_communities":        map[string]any{"type": "boolean"},
	})
}

func v2TraceMemoryInputSchema() map[string]any {
	return v2ContractInput([]string{"relationship_id"}, map[string]any{
		"relationship_id":          schemaString("Same-team Relationship ID.", 128),
		"include_evidence_content": map[string]any{"type": "boolean"},
		"max_edges":                map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		"max_chars":                map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
	})
}

func v2RecallFeedbackInputSchema() map[string]any {
	return v2ContractInput([]string{"recalls"}, map[string]any{
		"recalls": map[string]any{
			"type":     "array",
			"minItems": 1,
			"maxItems": 20,
			"items": map[string]any{
				"type":     "object",
				"required": []string{"recall_id", "used", "answer_supported", "quality"},
				"properties": map[string]any{
					"recall_id":        schemaString("Recall event ID.", 128),
					"used":             map[string]any{"type": "boolean"},
					"answer_supported": map[string]any{"type": "boolean"},
					"quality":          schemaEnum([]string{"high", "medium", "low"}),
					"missing_context":  map[string]any{"type": "boolean"},
					"irrelevant":       map[string]any{"type": "boolean"},
					"feedback_comment": schemaString("Bounded feedback note.", 1000),
				},
				"additionalProperties": false,
			},
		},
	})
}

func v2ListDreamsInputSchema() map[string]any {
	return v2ContractInput(nil, map[string]any{
		"status": schemaEnum([]string{"proposed", "reinforced", "stale", "rejected", "promoted"}),
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
	})
}

func v2GetDreamInputSchema() map[string]any {
	return v2ContractInput([]string{"dream_id"}, map[string]any{
		"dream_id": schemaString("Hypothesis ID.", 128),
	})
}

func v2ResolveDreamFeedbackInputSchema() map[string]any {
	return v2ContractInput([]string{"dream_id", "decision"}, map[string]any{
		"dream_id": schemaString("Hypothesis ID.", 128),
		"decision": schemaEnum([]string{
			"ignore",
			"reinforce",
			"stale",
			"reject",
			"confirm_true",
			"confirm_false",
		}),
		"feedback": schemaString("Evidence-backed feedback.", 1024),
	})
}

func v2ListCommunitiesInputSchema() map[string]any {
	return v2ContractInput(nil, map[string]any{
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"cursor": schemaString("Opaque page cursor.", 512),
	})
}

func v2FindMemoryPackCandidatesInputSchema() map[string]any {
	return v2ContractInput([]string{"query"}, map[string]any{
		"query": schemaString("Topic search query.", 512),
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
	})
}

func v2ExportMemoryPackInputSchema() map[string]any {
	return v2ContractInput([]string{"name"}, map[string]any{
		"name":             schemaString("Memory pack name.", 256),
		"description":      schemaString("Memory pack description.", 1024),
		"relationship_ids": v2StringArraySchema("Relationship ID to export.", 500, 128),
		"include_support":  map[string]any{"type": "boolean"},
	})
}

func v2InspectMemoryPackInputSchema() map[string]any {
	return v2ContractInput(nil, map[string]any{
		"artifact_json":       schemaString("Memory-pack JSON artifact.", 0),
		"url":                 schemaString("HTTPS URL.", 2048),
		"expected_sha256":     schemaString("Expected canonical SHA-256.", 64),
		"recommend_decisions": map[string]any{"type": "boolean"},
	})
}

func v2ImportMemoryPackInputSchema() map[string]any {
	return v2ContractInput([]string{"mode"}, map[string]any{
		"artifact_json":      schemaString("Memory-pack JSON artifact.", 0),
		"url":                schemaString("HTTPS URL.", 2048),
		"expected_sha256":    schemaString("Expected canonical SHA-256.", 64),
		"mode":               schemaEnum([]string{"review", "trusted"}),
		"selected_item_ids":  v2StringArraySchema("Artifact item ID selected for import.", 500, 128),
		"conflict_decisions": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	})
}

func v2RollbackMemoryPackImportInputSchema() map[string]any {
	return v2ContractInput([]string{"import_id"}, map[string]any{
		"import_id": schemaString("Memory-pack import ID.", 128),
	})
}

func v2PlacementRunOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ingest_id":           schemaString("Placement run ID.", 128),
			"status":              schemaEnum(domain.V2PlacementRunStatuses()),
			"check_after_seconds": map[string]any{"type": "integer"},
			"status_tool":         schemaString("Polling tool name.", 128),
			"items": map[string]any{
				"type":  "array",
				"items": v2PlacementItemSchema(),
			},
			"search_state": schemaEnum(domain.V2SearchProjectionStates()),
			"degradation":  v2DegradationSchema(),
		},
	}
}

func v2PlacementItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"item_id":               schemaString("Stable placement item ID.", 128),
			"evidence_index":        map[string]any{"type": "integer"},
			"category":              schemaEnum(domain.V2EvidenceItemCategories()),
			"relationship_outcomes": v2RelationshipOutcomeArraySchema(),
			"search_state":          schemaEnum(domain.V2SearchProjectionStates()),
			"degradation":           v2DegradationSchema(),
			"redacted_audit_ref":    schemaString("Redacted audit handle.", 128),
			"provider_event_ref":    schemaString("Redacted provider event handle.", 128),
		},
	}
}

func v2RelationshipOutcomeArraySchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category":          schemaEnum(domain.V2RelationshipOutcomeCategories()),
				"relationship_id":   schemaString("Relationship ID when one exists.", 128),
				"reason":            schemaString("Bounded placement explanation.", 1000),
				"predicate_options": map[string]any{"type": "array", "items": v2PredicateDefinitionSchema()},
			},
		},
	}
}

func v2ResolveMemoryPlacementOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"decision_id":         schemaString("Append-only decision ID.", 128),
			"ingest_id":           schemaString("Follow-up placement run ID.", 128),
			"status":              schemaEnum(domain.V2PlacementRunStatuses()),
			"impact_summary":      schemaString("Bounded before/after summary.", 1000),
			"check_after_seconds": map[string]any{"type": "integer"},
		},
	}
}

func v2CorrectEntityResolutionOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dry_run":        map[string]any{"type": "boolean"},
			"plan_token":     schemaString("Token required for apply.", 256),
			"selected_ids":   v2StringArraySchema("Observation selected for change.", 500, 128),
			"blocked_ids":    v2StringArraySchema("Observation blocked from change.", 500, 128),
			"impact_summary": schemaString("Bounded before/after summary.", 2000),
			"degradation":    v2DegradationSchema(),
		},
	}
}

func v2RecallMemoryOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"recall_id":          schemaString("Recall event ID.", 128),
			"results":            map[string]any{"type": "array", "items": v2RecallResultSchema()},
			"discovery_guidance": schemaString("Bounded follow-up guidance.", 1000),
			"degradation":        v2DegradationSchema(),
			"search_state":       schemaEnum(domain.V2SearchProjectionStates()),
		},
	}
}

func v2RecallResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"evidence_id":      schemaString("Evidence ID.", 128),
			"relationship_ids": v2StringArraySchema("Relationship ID.", 50, 128),
			"score":            map[string]any{"type": "number"},
			"rank":             map[string]any{"type": "integer"},
			"context":          schemaString("Bounded evidence context.", 2000),
		},
	}
}

func v2TraceMemoryOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"relationship":         map[string]any{"type": "object"},
			"evidence_supports":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"evidence_fragments":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"identity_corrections": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"supersession_lineage": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"degradation":          v2DegradationSchema(),
			"stopped_reason":       schemaString("Trace budget stop reason.", 128),
		},
	}
}

func v2DegradationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"required_failure": map[string]any{"type": "boolean"},
			"optional":         map[string]any{"type": "boolean"},
			"code":             schemaString("Typed degradation code.", 128),
			"message":          schemaString("Bounded public degradation message.", 512),
		},
		"additionalProperties": false,
	}
}

func v2PredicateDefinitionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"predicate_key",
			"version",
			"relationship_kind",
			"current_cardinality",
			"lifecycle_state",
		},
		"properties": map[string]any{
			"predicate_key":         schemaString("Immutable predicate key.", 128),
			"aliases":               v2StringArraySchema("Predicate alias.", 50, 128),
			"allowed_subject_kinds": v2StringArraySchema("Allowed subject Entity kind.", 20, 64),
			"allowed_object_kinds": v2StringArraySchema(
				"Allowed object Entity kind or Value type.",
				20,
				64,
			),
			"relationship_kind":   schemaEnum(domain.V2RelationshipKinds()),
			"current_cardinality": schemaEnum(domain.V2CurrentCardinalities()),
			"lifecycle_state":     schemaEnum(domain.V2PredicateLifecycleStates()),
			"version":             map[string]any{"type": "integer", "minimum": 1},
		},
	}
}

func v2StringArraySchema(description string, maxItems int, maxLen int) map[string]any {
	schema := map[string]any{
		"type":  "array",
		"items": schemaString(description, maxLen),
	}
	if maxItems > 0 {
		schema["maxItems"] = maxItems
	}
	return schema
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

func assertV2ProviderProposalSchema(schema map[string]any) error {
	props := schemaProperties(schema)
	if len(props) == 0 {
		return errors.New("provider proposal schema has no properties")
	}
	for _, forbidden := range []string{"team_id", "profile_id", "tier", "status", "predicate_definitions"} {
		if _, ok := props[forbidden]; ok {
			return fmt.Errorf("provider proposal schema allows %s", forbidden)
		}
	}
	if _, ok := props["predicate_options"]; !ok {
		return errors.New("provider proposal schema has no predicate_options")
	}
	relationshipProposals, ok := props["relationship_proposals"]
	if !ok {
		return errors.New("provider proposal schema has no relationship_proposals")
	}
	items, ok := relationshipProposals["items"].(map[string]any)
	if !ok {
		return errors.New("provider relationship_proposals schema has no item schema")
	}
	oneOf, ok := items["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		return errors.New("provider relationship proposal schema must require exactly one object form")
	}
	return nil
}
