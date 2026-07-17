package registry

import (
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
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
	V2ToolListCommunities,
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
			V2ToolListCommunities,
			"List bounded same-team community summaries as derived read-model data.",
			[]string{"read"},
			v2ListCommunitiesInputSchema(),
			v2ListCommunitiesOutputSchema(),
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
	switch domain.V2ResolveAction(action) {
	case domain.V2ResolveAcknowledge:
		return validateV2RequiredFields(args, "ingest_id")
	case domain.V2ResolveSelectEntity:
		if err := validateV2RequiredFields(args, "ingest_id", "placement_item_id", "decision"); err != nil {
			return err
		}
		return validateV2DecisionFields(args, "mention_ref", "entity_id")
	case domain.V2ResolveConfirmNewEntity:
		if err := validateV2RequiredFields(args, "ingest_id", "placement_item_id", "decision", "evidence"); err != nil {
			return err
		}
		return validateV2DecisionFields(args, "mention_ref")
	case domain.V2ResolveSelectPredicate:
		if err := validateV2RequiredFields(args, "ingest_id", "placement_item_id", "decision"); err != nil {
			return err
		}
		return validateV2DecisionFields(args, "observation_id", "predicate_key", "predicate_version")
	case domain.V2ResolveAccept:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "evidence")
	case domain.V2ResolveReject:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "reason")
	case domain.V2ResolveCorrect:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "evidence")
	case domain.V2ResolveReleaseQuarantine:
		return validateV2RequiredFields(args, "ingest_id", "placement_item_id", "reason")
	case domain.V2ResolveForget:
		return validateV2RequiredFields(args, "relationship_id", "reason", "evidence")
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

func v2ContractInput(required []string, properties map[string]any) map[string]any {
	v2RequireNonEmptyStrings(required, properties)
	return map[string]any{
		"type":                 "object",
		"required":             required,
		"properties":           properties,
		"additionalProperties": false,
		"x-contract-version":   domain.V2ContractVersion,
	}
}

func v2RememberInputSchema() map[string]any {
	return v2ContractInput([]string{"evidence"}, map[string]any{
		"evidence": v2EvidenceArraySchema(),
		"proposal": v2ClosedObject(nil, map[string]any{
			"entities":      v2EntityProposalArraySchema(),
			"relationships": v2RelationshipProposalArraySchema(),
		}),
	})
}

func v2EvidenceArraySchema() map[string]any {
	content := memoryEntryString("Verbatim evidence text.")
	content["minLength"] = 1
	return map[string]any{
		"type":     "array",
		"minItems": 1,
		"maxItems": 20,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"content"},
			"properties": map[string]any{
				"content":                  content,
				"source_type":              schemaEnum([]string{"conversation", "document", "observation", "manual"}),
				"source":                   schemaString("Bounded provenance label.", 256),
				"authority":                schemaEnum([]string{"authoritative", "primary", "secondary", "inferred", "unknown"}),
				"source_group":             schemaString("Proposed evidence source grouping.", 256),
				"source_key":               schemaString("Stable source-owner-scoped identity.", 256),
				"source_revision":          schemaString("Opaque current source revision token.", 256),
				"previous_source_revision": schemaString("Exact previous source revision token.", 256),
				"supersedes_fragment_ids":  v2StringArraySchema("Evidence fragment ID to supersede.", 50, 128),
				"idempotency_key":          schemaString("Evidence retry key scoped to team and profile.", 128),
				"labels":                   v2StringArraySchema("Evidence label.", 20, 64),
				"metadata":                 v2BoundedMap("Source-policy-approved metadata."),
			},
			"additionalProperties": false,
		},
	}
}

func v2EntityProposalArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 100,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"ref", "name"},
			"properties": map[string]any{
				"ref":              v2NonEmptyString("Client-local Entity proposal ref.", 128),
				"name":             v2NonEmptyString("Evidence-supported name.", 256),
				"entity_kind":      schemaEnum(domain.V2EntityKinds()),
				"aliases":          v2StringArraySchema("Evidence-supported alias.", 20, 256),
				"identity_context": v2BoundedMap("Evidence-supported identity discriminators."),
				"known_entity_id":  v2NullableString("Server-issued Entity candidate hint.", 128),
			},
			"additionalProperties": false,
		},
	}
}

func v2RelationshipProposalArraySchema() map[string]any {
	return map[string]any{
		"type":     "array",
		"maxItems": 200,
		"items": map[string]any{
			"type":     "object",
			"required": []string{"proposal_id", "subject_ref", "predicate", "evidence"},
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
				"proposal_id": v2NonEmptyString("Client-local Relationship proposal ref.", 128),
				"subject_ref": v2NonEmptyString("Entity proposal ref.", 128),
				"predicate":   v2NonEmptyString("Registered predicate name.", 128),
				"object_ref":  schemaString("Entity proposal ref.", 128),
				"object_value": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"type":    schemaEnum(domain.V2ValueTypes()),
						"value":   map[string]any{"type": []any{"string", "number", "boolean"}},
						"display": schemaString("Optional display form.", 1024),
						"unit":    schemaString("Optional unit.", 128),
					},
					"required": []string{"type", "value"},
				},
				"polarity": schemaEnum([]string{"+", "-"}),
				"modality": schemaEnum([]string{"statement", "question", "proposal", "speculation", "quoted"}),
				"evidence": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": 20,
					"items": map[string]any{
						"type":                 "object",
						"required":             []string{"evidence_index", "start", "end"},
						"additionalProperties": false,
						"properties": map[string]any{
							"evidence_index": map[string]any{"type": "integer", "minimum": 0},
							"start":          map[string]any{"type": "integer", "minimum": 0},
							"end":            map[string]any{"type": "integer", "minimum": 0},
						},
					},
					"valid_from":     v2NullableDateTime("Evidence-supported validity start."),
					"valid_to":       v2NullableDateTime("Evidence-supported validity end."),
					"client_comment": v2NullableString("Non-authoritative extraction note.", 1000),
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
		"relationship_id":   schemaString("Caller-owned Relationship ID.", 128),
		"decision": v2ClosedObject(nil, map[string]any{
			"mention_ref":       schemaString("Client-local Entity mention ref.", 128),
			"entity_id":         schemaString("Server-supplied same-team Entity ID.", 128),
			"observation_id":    schemaString("Unresolved observation ID.", 128),
			"predicate_key":     schemaString("Server-supplied registered predicate key.", 128),
			"predicate_version": map[string]any{"type": "integer", "minimum": 1},
		}),
		"reason":   schemaString("Bounded reviewer reason.", 1000),
		"evidence": v2EvidenceArraySchema(),
		"proposal": v2ClosedObject(nil, map[string]any{
			"entities":      v2EntityProposalArraySchema(),
			"relationships": v2RelationshipProposalArraySchema(),
		}),
		"idempotency_key": schemaString("Resolution retry key scoped to team and profile.", 128),
	})
}

func v2CorrectEntityResolutionInputSchema() map[string]any {
	return v2ContractInput(
		[]string{"operation", "source_entity_id", "target_entity_id", "owned_observation_ids", "evidence", "dry_run", "idempotency_key"},
		map[string]any{
			"operation":             schemaEnum(domain.V2EntityCorrectionActions()),
			"source_entity_id":      schemaString("Caller-visible source Entity ID.", 128),
			"target_entity_id":      v2NullableString("Merge target Entity ID; null for split.", 128),
			"owned_observation_ids": v2StringArraySchema("Caller-owned observation ID.", 200, 128),
			"dry_run":               map[string]any{"type": "boolean"},
			"impact_token":          schemaString("Version-bound dry-run token required for apply.", 256),
			"evidence":              v2EvidenceArraySchema(),
			"idempotency_key":       schemaString("Correction retry key scoped to team and profile.", 128),
		},
	)
}

func v2RecallMemoryInputSchema() map[string]any {
	query := schemaString("Natural-language recall query.", 512)
	query["minLength"] = 1
	return v2ContractInput([]string{"query"}, map[string]any{
		"query":                  query,
		"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"valid_at":               v2NullableDateTime("Real-world recall time."),
		"known_at":               v2NullableDateTime("System-knowledge recall time."),
		"known_evidence_ids":     v2StringArraySchema("Evidence UUID already seen.", 200, 128),
		"known_relationship_ids": v2StringArraySchema("Relationship UUID already seen.", 200, 128),
		"expand_from_entity_ids": v2StringArraySchema("Entity UUID for focused expansion.", 50, 128),
	})
}

func v2TraceMemoryInputSchema() map[string]any {
	return v2ContractInput([]string{"relationship_id"}, map[string]any{
		"relationship_id":          schemaString("Same-team Relationship ID.", 128),
		"include_evidence_content": map[string]any{"type": "boolean"},
		"include_verification":     map[string]any{"type": "boolean"},
		"include_transitions":      map[string]any{"type": "boolean"},
		"max_depth":                map[string]any{"type": "integer", "minimum": 0, "maximum": 4},
		"max_edges":                map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"predicate_keys":           v2StringArraySchema("Registered predicate key filter.", 30, 128),
		"topic":                    v2NullableString("Optional graph-context topic.", 256),
		"min_relevance":            v2NullableNumber("Optional graph-context relevance threshold.", 0, 1),
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
				"required": []string{"recall_event_id", "used", "answer_supported", "quality"},
				"properties": map[string]any{
					"recall_event_id":  v2NonEmptyString("Recall event ID.", 128),
					"used":             map[string]any{"type": "boolean"},
					"answer_supported": map[string]any{"type": "boolean"},
					"quality":          schemaEnum([]string{"high", "medium", "low"}),
					"missing_context":  map[string]any{"type": "boolean"},
					"irrelevant":       map[string]any{"type": "boolean"},
					"feedback_comment": schemaString("Bounded feedback note.", 1000),
					"irrelevant_result_refs": v2Array(v2ClosedObject(
						[]string{"type", "id", "rank"},
						map[string]any{
							"type": schemaEnum([]string{"relationship", "fragment", "community", "hypothesis"}),
							"id":   schemaString("Result ID from the recall event.", 128),
							"rank": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
						},
					), 0, 100),
					"hypothesis_feedback": v2Array(v2ClosedObject(
						[]string{"hypothesis_id", "used", "quality", "contradicted"},
						map[string]any{
							"hypothesis_id":    schemaString("Hypothesis ID returned with recall.", 128),
							"used":             map[string]any{"type": "boolean"},
							"quality":          schemaEnum([]string{"high", "medium", "low"}),
							"contradicted":     map[string]any{"type": "boolean"},
							"feedback_comment": schemaString("Bounded Hypothesis feedback note.", 1000),
						},
					), 0, 100),
				},
				"additionalProperties": false,
			},
		},
	})
}

func v2ListDreamsInputSchema() map[string]any {
	return v2ContractInput(nil, map[string]any{
		"status": schemaEnum([]string{"proposed", "reinforced", "stale", "rejected", "submitted"}),
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"cursor": schemaString("Opaque page cursor.", 512),
	})
}

func v2GetDreamInputSchema() map[string]any {
	return v2ContractInput([]string{"hypothesis_id"}, map[string]any{
		"hypothesis_id": schemaString("Hypothesis ID.", 128),
	})
}

func v2ResolveDreamFeedbackInputSchema() map[string]any {
	return v2ContractInput([]string{"hypothesis_id", "decision"}, map[string]any{
		"hypothesis_id": schemaString("Hypothesis ID.", 128),
		"decision": schemaEnum([]string{
			"reinforce",
			"stale",
			"reject",
			"confirm_true",
			"confirm_false",
		}),
		"reason":   schemaString("Bounded lifecycle feedback reason.", 1000),
		"evidence": v2EvidenceArraySchema(),
		"proposal": v2ClosedObject(nil, map[string]any{
			"entities":      v2EntityProposalArraySchema(),
			"relationships": v2RelationshipProposalArraySchema(),
		}),
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
		"query":          schemaString("Topic search query.", 512),
		"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"predicate_keys": v2StringArraySchema("Registered predicate key filter.", 100, 128),
	})
}

func v2ExportMemoryPackInputSchema() map[string]any {
	return v2ContractInput([]string{"name"}, map[string]any{
		"name":                 schemaString("Memory pack name.", 256),
		"description":          schemaString("Memory pack description.", 1024),
		"relationship_ids":     v2StringArraySchema("Relationship ID to export.", 500, 128),
		"include_evidence":     map[string]any{"type": "boolean"},
		"include_entity_names": map[string]any{"type": "boolean"},
	})
}

func v2InspectMemoryPackInputSchema() map[string]any {
	return v2ContractInput([]string{"mode"}, map[string]any{
		"artifact_json":   schemaString("Memory-pack JSON artifact.", v2MemoryPackArtifactMaxLength),
		"url":             schemaString("HTTPS URL.", 2048),
		"expected_sha256": schemaString("Expected canonical SHA-256.", 64),
		"mode":            schemaEnum([]string{"review", "trusted"}),
	})
}

func v2ImportMemoryPackInputSchema() map[string]any {
	return v2ContractInput([]string{"mode"}, map[string]any{
		"artifact_json":   schemaString("Memory-pack JSON artifact.", v2MemoryPackArtifactMaxLength),
		"url":             schemaString("HTTPS URL.", 2048),
		"expected_sha256": schemaString("Expected canonical SHA-256.", 64),
		"mode":            schemaEnum([]string{"review", "trusted"}),
		"conflict_decisions": v2Array(v2ClosedObject(
			[]string{"item_id", "decision"},
			map[string]any{
				"item_id":          schemaString("Artifact-local item ID.", 128),
				"decision":         schemaEnum([]string{"skip", "import_for_review", "map_entity", "confirm_new_entity", "accept_source_authority"}),
				"target_entity_id": schemaString("Server-supplied same-team Entity candidate.", 128),
				"evidence":         v2EvidenceArraySchema(),
			},
		), 0, 500),
		"idempotency_key": schemaString("Import retry key scoped to team and profile.", 128),
	})
}

func v2RollbackMemoryPackImportInputSchema() map[string]any {
	return v2ContractInput([]string{"import_id", "dry_run"}, map[string]any{
		"import_id":    schemaString("Memory-pack import ID.", 128),
		"dry_run":      map[string]any{"type": "boolean"},
		"impact_token": schemaString("Version-bound rollback dry-run token required for apply.", 256),
	})
}

func v2RememberOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"ingest_id", "processing_state", "check_after_seconds", "status_tool", "correlation_id"},
		map[string]any{
			"ingest_id":           schemaString("Placement run ID.", 128),
			"processing_state":    schemaEnum(domain.V2PlacementRunStatuses()),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
			"status_tool":         schemaEnum([]string{V2ToolGetMemoryPlacement}),
			"correlation_id":      schemaString("Request correlation ID.", 128),
		},
	)
}

func v2PlacementRunOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"ingest_id", "processing_state", "search_state", "items", "errors"},
		map[string]any{
			"ingest_id":        schemaString("Placement run ID.", 128),
			"processing_state": schemaEnum(domain.V2PlacementRunStatuses()),
			"search_state":     schemaEnum(domain.V2SearchProjectionStates()),
			"items":            v2Array(v2PlacementItemSchema(), 0, 20),
			"errors":           v2PlacementErrorArraySchema(),
		},
	)
}

func v2PlacementItemSchema() map[string]any {
	return v2ClosedObject(
		[]string{"item_id", "evidence_id", "category", "search_state", "relationship_outcomes", "errors"},
		map[string]any{
			"item_id":               schemaString("Stable placement item ID.", 128),
			"evidence_id":           schemaString("Durable evidence fragment ID.", 128),
			"category":              schemaEnum(domain.V2EvidenceItemCategories()),
			"relationship_outcomes": v2RelationshipOutcomeArraySchema(),
			"search_state":          schemaEnum(domain.V2SearchProjectionStates()),
			"errors":                v2PlacementErrorArraySchema(),
		},
	)
}

func v2RelationshipOutcomeArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 200,
		"items": v2ClosedObject(
			[]string{"proposal_id", "observation_id", "owner_profile_id", "category", "reason"},
			map[string]any{
				"proposal_id":         v2NullableString("Client proposal ID when supplied.", 128),
				"observation_id":      v2NullableString("Durable observation ID when one exists.", 128),
				"relationship_id":     v2NullableString("Relationship ID when one exists.", 128),
				"owner_profile_id":    schemaString("Relationship owner profile ID.", 128),
				"tier":                schemaEnum(domain.V2RelationshipTiers()),
				"relationship_status": schemaEnum(domain.V2RelationshipStatuses()),
				"category":            schemaEnum(domain.V2RelationshipOutcomeCategories()),
				"reason":              schemaString("Bounded placement explanation.", 1000),
				"review_task":         v2NullableString("Review task handle.", 128),
				"predicate_options":   v2Array(v2PredicateDefinitionSchema(), 0, 100),
			},
		),
	}
}

func v2ResolveMemoryPlacementOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"decision_id", "processing_state"},
		map[string]any{
			"decision_id":         schemaString("Append-only decision ID.", 128),
			"ingest_id":           schemaString("Follow-up placement run ID.", 128),
			"processing_state":    schemaEnum(domain.V2PlacementRunStatuses()),
			"impact_summary":      schemaString("Bounded before/after summary.", 1000),
			"check_after_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 3600},
		},
	)
}

func v2CorrectEntityResolutionOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{
			"dry_run",
			"selected_observation_ids",
			"blocked_observation_ids",
			"relationship_changes",
			"entity_candidates",
			"unchanged_cross_profile_references",
		},
		map[string]any{
			"dry_run":                            map[string]any{"type": "boolean"},
			"impact_token":                       schemaString("Expiring version-bound apply token.", 256),
			"selected_observation_ids":           v2StringArraySchema("Observation selected for change.", 200, 128),
			"blocked_observation_ids":            v2StringArraySchema("Observation blocked from change.", 200, 128),
			"relationship_changes":               v2Array(v2CorrectionRelationshipChangeSchema(), 0, 500),
			"entity_candidates":                  v2Array(v2CorrectionEntityCandidateSchema(), 0, 100),
			"unchanged_cross_profile_references": v2Array(v2CrossProfileReferenceSchema(), 0, 500),
		},
	)
}

func v2RecallMemoryOutputSchema() map[string]any {
	return v2ClosedObject(
		[]string{"recall_id", "results", "discovery_paths", "discovery_guidance", "related_hypotheses"},
		map[string]any{
			"recall_id":          schemaString("Recall event ID.", 128),
			"results":            v2Array(v2RecallResultSchema(), 0, 50),
			"discovery_paths":    v2Array(v2RecallDiscoveryPathSchema(), 0, 50),
			"discovery_guidance": schemaString("Bounded follow-up guidance.", 1000),
			"related_hypotheses": v2Array(v2HypothesisSummarySchema(), 0, 20),
		},
	)
}

func v2RecallResultSchema() map[string]any {
	return v2ClosedObject(
		[]string{"evidence_id", "context"},
		map[string]any{
			"evidence_id": schemaString("Evidence ID.", 128),
			"context":     schemaString("Bounded evidence context.", 2000),
		},
	)
}

func v2TraceMemoryOutputSchema() map[string]any {
	required := []string{
		"relationship", "observations", "evidence_supports", "support_decision_events",
		"evidence_fragments", "verification_events", "transitions", "conflicts",
		"cross_profile_references", "identity_corrections", "supersession_lineage",
		"semantic_nodes", "semantic_edges", "visited_entity_ids", "stopped_reason",
	}
	return v2ClosedObject(required, map[string]any{
		"relationship":             v2TraceRelationshipSchema(),
		"observations":             v2Array(v2TraceObservationSchema(), 0, 500),
		"evidence_supports":        v2Array(v2TraceEvidenceSupportSchema(), 0, 500),
		"support_decision_events":  v2Array(v2TraceSupportDecisionSchema(), 0, 500),
		"evidence_fragments":       v2Array(v2TraceEvidenceFragmentSchema(), 0, 500),
		"verification_events":      v2Array(v2TraceVerificationSchema(), 0, 500),
		"transitions":              v2Array(v2TraceTransitionSchema(), 0, 500),
		"conflicts":                v2Array(v2TraceConflictSchema(), 0, 500),
		"cross_profile_references": v2Array(v2CrossProfileReferenceSchema(), 0, 500),
		"identity_corrections":     v2Array(v2TraceIdentityCorrectionSchema(), 0, 500),
		"supersession_lineage":     v2Array(v2TraceRelationshipSchema(), 0, 500),
		"semantic_nodes":           v2Array(v2SemanticNodeSchema(), 0, 500),
		"semantic_edges":           v2Array(v2SemanticEdgeSchema(), 0, 500),
		"visited_entity_ids":       v2StringArraySchema("Visited Entity ID.", 500, 128),
		"stopped_reason":           v2NullableString("Trace budget stop reason.", 128),
	})
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
	item := schemaString(description, maxLen)
	item["minLength"] = 1
	schema := map[string]any{
		"type":        "array",
		"items":       item,
		"uniqueItems": true,
	}
	if maxItems > 0 {
		schema["maxItems"] = maxItems
	}
	return schema
}
