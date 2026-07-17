package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type v2ContractFixture struct {
	Name            string         `json:"name"`
	Tool            string         `json:"tool"`
	ContractVersion string         `json:"contract_version"`
	Scopes          []string       `json:"scopes"`
	Valid           bool           `json:"valid"`
	WantError       string         `json:"want_error"`
	Input           map[string]any `json:"input"`
	Output          map[string]any `json:"output"`
}

func TestV2ContractFixtures(t *testing.T) {
	tools := v2ToolMap(t)
	fixtures := readV2ContractFixtures(t)

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			tool, err := requireV2Tool(tools, fixture.Tool)
			if err != nil {
				t.Fatal(err)
			}
			if fixture.ContractVersion != "" {
				tool.ContractVersion = fixture.ContractVersion
			}
			err = ValidateV2ContractInput(tool, fixture.Input, fixture.Scopes)
			if fixture.Valid {
				if err != nil {
					t.Fatalf("ValidateV2ContractInput: %v", err)
				}
				if fixture.Output == nil {
					t.Fatal("valid fixture has no output")
				}
				if err := ValidateInput(Tool{InputSchema: tool.OutputSchema}, fixture.Output); err != nil {
					t.Fatalf("validate output: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateV2ContractInput succeeded, want error")
			}
			if fixture.WantError != "" && !strings.Contains(err.Error(), fixture.WantError) {
				t.Fatalf("error = %q, want containing %q", err.Error(), fixture.WantError)
			}
		})
	}
}

func TestV2ContractRememberBoundaryContent(t *testing.T) {
	tools := v2ToolMap(t)
	remember, err := requireV2Tool(tools, V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"evidence": []any{
			map[string]any{
				"content": strings.Repeat("a", memoryEntryMaxLength),
			},
		},
	}
	if err := ValidateV2ContractInput(remember, input, []string{"write"}); err != nil {
		t.Fatalf("boundary content rejected: %v", err)
	}
	input["evidence"] = []any{
		map[string]any{
			"content": strings.Repeat("a", memoryEntryMaxLength+1),
		},
	}
	err = ValidateV2ContractInput(remember, input, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "maximum length") {
		t.Fatalf("oversized content err = %v, want maximum length", err)
	}
}

func TestV2ContractVersionIsMetadataNotPayload(t *testing.T) {
	for _, tool := range V2ContractTools() {
		props := schemaProperties(tool.InputSchema)
		if _, ok := props["contract_version"]; ok {
			t.Fatalf("%s exposes contract_version as a payload field", tool.Name)
		}
		if got := tool.InputSchema["x-contract-version"]; got != domain.V2ContractVersion {
			t.Fatalf("%s x-contract-version = %v", tool.Name, got)
		}
	}
}

func TestIsV2ContractToolDoesNotTrustVersionBeforeValidation(t *testing.T) {
	tool := V2ContractTools()[0]
	tool.ContractVersion = "dense-mem.v2.0"
	if !IsV2ContractTool(tool) {
		t.Fatal("wrong-version V2 descriptor bypasses V2 transport validation")
	}
}

func TestV2ContractCatalogMetadata(t *testing.T) {
	tools := V2ContractTools()
	if len(tools) != len(V2ContractToolNames()) {
		t.Fatalf("tool names length mismatch")
	}
	seen := map[string]struct{}{}
	for _, tool := range tools {
		if _, dup := seen[tool.Name]; dup {
			t.Fatalf("duplicate V2 tool name %s", tool.Name)
		}
		seen[tool.Name] = struct{}{}
		if tool.ContractVersion != domain.V2ContractVersion {
			t.Fatalf("%s contract version = %q", tool.Name, tool.ContractVersion)
		}
		if tool.FeatureGate != domain.V2FeatureGate || tool.Visibility != domain.V2ToolVisibility {
			t.Fatalf("%s gate metadata = %q/%q", tool.Name, tool.FeatureGate, tool.Visibility)
		}
		if len(tool.RequiredScopes) == 0 {
			t.Fatalf("%s has no required scopes", tool.Name)
		}
		if tool.Invoke != nil {
			t.Fatalf("%s has an invoker before the V2 gate is wired", tool.Name)
		}
	}
	for _, name := range []string{
		V2ToolRemember,
		V2ToolResolveMemoryPlacement,
		V2ToolCorrectEntityResolution,
		V2ToolRecallMemory,
		V2ToolTraceMemory,
		V2ToolListCommunities,
		V2ToolImportMemoryPack,
	} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("V2 contract missing tool %s", name)
		}
	}
}

func TestV2InputSchemasOmitEmptyRequired(t *testing.T) {
	for _, tool := range V2ContractTools() {
		required := schemaRequiredFields(tool.InputSchema)
		if len(required) == 0 {
			if _, ok := tool.InputSchema["required"]; ok {
				t.Fatalf("%s input schema has empty required keyword", tool.Name)
			}
			continue
		}
		if _, ok := tool.InputSchema["required"]; !ok {
			t.Fatalf("%s input schema omits non-empty required fields", tool.Name)
		}
	}
}

func TestToolVisibleHidesDormantV2ContractTools(t *testing.T) {
	if !ToolVisible(context.Background(), Tool{Name: "recall_memory"}, nil) {
		t.Fatal("ordinary tools should remain visible")
	}
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	if ToolVisible(context.Background(), remember, nil) {
		t.Fatal("dormant V2 contract tool is visible before cutover")
	}
	remember.Visibility = "active"
	if !ToolVisible(context.Background(), remember, nil) {
		t.Fatal("non-dormant V2 tool should be visible to cutover wiring")
	}
}

func TestBuildDefaultDoesNotExposeV2ContractTools(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	if _, ok := reg.Get(V2ToolCorrectEntityResolution); ok {
		t.Fatal("BuildDefault exposed correct_entity_resolution before V2 gate")
	}
	for _, tool := range reg.List() {
		if tool.ContractVersion == domain.V2ContractVersion {
			t.Fatalf("BuildDefault exposed V2 contract metadata on %s", tool.Name)
		}
	}
}

func TestBuildV2UATWiresExecutableRemember(t *testing.T) {
	stub := &stubV2RememberService{}
	reg, err := BuildV2UAT(Dependencies{V2Remember: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	remember, ok := reg.Get(V2ToolRemember)
	if !ok {
		t.Fatal("BuildV2UAT did not register remember")
	}
	if remember.Invoke == nil {
		t.Fatal("BuildV2UAT remember invoker is nil")
	}
	out, err := remember.Invoke(context.Background(), "ignored-profile", map[string]any{
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err != nil {
		t.Fatalf("remember.Invoke: %v", err)
	}
	if out["ingest_id"] != "ingest-v2" {
		t.Fatalf("ingest_id = %#v, want ingest-v2", out["ingest_id"])
	}
	if stub.req.ContractVersion != domain.V2ContractVersion || len(stub.req.Evidence) != 1 {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.Evidence[0].Content != "remember this exact evidence" {
		t.Fatalf("evidence content = %q", stub.req.Evidence[0].Content)
	}
}

func TestBuildV2UATRememberRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Remember: &stubV2RememberService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	remember, ok := reg.Get(V2ToolRemember)
	if !ok {
		t.Fatal("BuildV2UAT did not register remember")
	}
	_, err = remember.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("remember.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATWiresExecutableRecallMemory(t *testing.T) {
	stub := &stubV2RecallService{}
	reg, err := BuildV2UAT(Dependencies{V2Recall: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	recall, ok := reg.Get(V2ToolRecallMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register recall_memory")
	}
	if recall.Invoke == nil {
		t.Fatal("BuildV2UAT recall_memory invoker is nil")
	}
	out, err := recall.Invoke(context.Background(), "ignored-profile", map[string]any{
		"query": "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("recall_memory.Invoke: %v", err)
	}
	if out["recall_id"] != "rec-v2" {
		t.Fatalf("recall_id = %#v, want rec-v2", out["recall_id"])
	}
	results, ok := out["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v, want one result", out["results"])
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", results[0])
	}
	if _, ok := result["score"]; ok {
		t.Fatalf("recall_memory exposed score in public output: %#v", result)
	}
	if stub.req.Query != "PostgreSQL memory" {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.ContractVersion != domain.V2ContractVersion {
		t.Fatalf("contract version = %q", stub.req.ContractVersion)
	}
}

func TestBuildV2UATRecallRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Recall: &stubV2RecallService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	recall, ok := reg.Get(V2ToolRecallMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register recall_memory")
	}
	_, err = recall.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"query":   "PostgreSQL memory",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("recall_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestV2RememberRejectsMixedSourceRevisionBatch(t *testing.T) {
	tools := v2ToolMap(t)
	remember, err := requireV2Tool(tools, V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateV2ContractInput(remember, map[string]any{
		"evidence": []any{
			map[string]any{
				"content":         "first source fragment",
				"source_key":      "wiki://write-pipeline",
				"source_revision": "rev-1",
			},
			map[string]any{
				"content":         "second source fragment",
				"source_key":      "wiki://write-pipeline",
				"source_revision": "rev-2",
			},
		},
	}, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "revision fields must match") {
		t.Fatalf("ValidateV2ContractInput err = %v, want mixed source revision rejection", err)
	}
}

func TestV2RelationshipProposalsRequireExactlyOneObject(t *testing.T) {
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	baseInput := map[string]any{
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL."},
		},
	}
	baseProposal := map[string]any{
		"proposal_id": "rel-1",
		"subject_ref": "entity-1",
		"predicate":   "uses",
		"evidence": []any{
			map[string]any{"evidence_index": 0, "start": 10, "end": 25},
		},
	}

	cases := []struct {
		name     string
		proposal map[string]any
	}{
		{
			name:     "missing object",
			proposal: cloneMap(baseProposal),
		},
		{
			name: "both object forms",
			proposal: mergeMap(baseProposal, map[string]any{
				"object_ref": "entity-2",
				"object_value": map[string]any{
					"type":  "string",
					"value": "PostgreSQL",
				},
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := cloneMap(baseInput)
			input["proposal"] = map[string]any{
				"entities": []any{
					map[string]any{"ref": "entity-1", "name": "Dense-Mem"},
				},
				"relationships": []any{tc.proposal},
			}
			err := ValidateV2ContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), "exactly one of object_ref or object_value") {
				t.Fatalf("ValidateV2ContractInput err = %v, want object choice error", err)
			}
		})
	}
}

func TestV2ResolveMemoryPlacementActionRequiredFields(t *testing.T) {
	resolve, err := requireV2Tool(v2ToolMap(t), V2ToolResolveMemoryPlacement)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"idempotency_key": "resolution-1"}
	evidence := []any{map[string]any{"content": "The user supplied authoritative resolution evidence."}}
	cases := []struct {
		name            string
		action          string
		valid           map[string]any
		missing         string
		missingDecision string
	}{
		{
			name:    "acknowledge requires ingest",
			action:  string(domain.V2ResolveAcknowledge),
			valid:   map[string]any{"ingest_id": "ing-1"},
			missing: "ingest_id",
		},
		{
			name:   "select entity requires mention and candidate",
			action: string(domain.V2ResolveSelectEntity),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"decision": map[string]any{
					"mention_ref": "person-1",
					"entity_id":   "ent-1",
				},
			},
			missingDecision: "entity_id",
		},
		{
			name:   "confirm new entity requires evidence",
			action: string(domain.V2ResolveConfirmNewEntity),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"decision":          map[string]any{"mention_ref": "person-1"},
				"evidence":          evidence,
			},
			missing: "evidence",
		},
		{
			name:   "select predicate requires predicate version",
			action: string(domain.V2ResolveSelectPredicate),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"decision": map[string]any{
					"observation_id":    "obs-1",
					"predicate_key":     "works_on",
					"predicate_version": float64(1),
				},
			},
			missingDecision: "predicate_version",
		},
		{
			name:   "accept requires evidence",
			action: string(domain.V2ResolveAccept),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"evidence":          evidence,
			},
			missing: "evidence",
		},
		{
			name:   "reject requires reason",
			action: string(domain.V2ResolveReject),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"reason":            "not supported by the source",
			},
			missing: "reason",
		},
		{
			name:   "correct requires evidence",
			action: string(domain.V2ResolveCorrect),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"evidence":          evidence,
			},
			missing: "evidence",
		},
		{
			name:   "release quarantine requires reason",
			action: string(domain.V2ResolveReleaseQuarantine),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"reason":            "manager reviewed quarantine signal",
			},
			missing: "reason",
		},
		{
			name:   "forget requires relationship target",
			action: string(domain.V2ResolveForget),
			valid: map[string]any{
				"relationship_id": "rel-1",
				"reason":          "user requested retraction",
				"evidence":        evidence,
			},
			missing: "relationship_id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid := mergeMap(base, tc.valid)
			valid["action"] = tc.action
			if err := ValidateV2ContractInput(resolve, valid, []string{"write"}); err != nil {
				t.Fatalf("valid action rejected: %v", err)
			}

			invalid := cloneMap(valid)
			missing := tc.missing
			if tc.missingDecision != "" {
				decision := cloneMap(invalid["decision"].(map[string]any))
				delete(decision, tc.missingDecision)
				invalid["decision"] = decision
				missing = "decision." + tc.missingDecision
			} else {
				delete(invalid, tc.missing)
			}
			err := ValidateV2ContractInput(resolve, invalid, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), missing+" is required") {
				t.Fatalf("missing %s err = %v", missing, err)
			}
		})
	}
}

func TestV2DefaultRecallDoesNotPermitCandidatesOrHypotheses(t *testing.T) {
	tools := v2ToolMap(t)
	recall, err := requireV2Tool(tools, V2ToolRecallMemory)
	if err != nil {
		t.Fatal(err)
	}
	props := schemaProperties(recall.OutputSchema)
	if _, ok := props["hypotheses"]; ok {
		t.Fatal("recall_memory output exposes hypotheses in default recall")
	}
	if _, ok := props["candidates"]; ok {
		t.Fatal("recall_memory output exposes candidates in default recall")
	}
	results, ok := props["results"]["items"].(map[string]any)
	if !ok {
		t.Fatalf("results schema missing item object: %#v", props["results"])
	}
	resultProps := schemaProperties(results)
	for _, forbidden := range []string{"hypothesis_id", "candidate_relationship_id"} {
		if _, ok := resultProps[forbidden]; ok {
			t.Fatalf("recall result exposes %s", forbidden)
		}
	}
	for _, required := range []string{"discovery_paths", "discovery_guidance", "related_hypotheses"} {
		if _, ok := props[required]; !ok {
			t.Fatalf("recall output missing %s", required)
		}
	}
	for _, internal := range []string{"search_state", "degradation"} {
		if _, ok := props[internal]; ok {
			t.Fatalf("recall output exposes internal field %s", internal)
		}
	}
}

func TestV2CanonicalInputFieldNames(t *testing.T) {
	tools := v2ToolMap(t)
	cases := []struct {
		tool      string
		required  []string
		forbidden []string
	}{
		{V2ToolRemember, []string{"evidence", "proposal"}, []string{"contract_version", "entity_hints", "relationship_hints"}},
		{V2ToolCorrectEntityResolution, []string{"operation", "owned_observation_ids", "impact_token"}, []string{"action", "selected_observation_ids", "plan_token"}},
		{V2ToolRecallMemory, []string{"known_evidence_ids", "known_relationship_ids", "expand_from_entity_ids"}, []string{"include_evidence", "use_communities"}},
		{V2ToolTraceMemory, []string{"include_verification", "include_transitions", "max_depth", "predicate_keys", "topic", "min_relevance"}, []string{"max_chars"}},
		{V2ToolSubmitRecallSessionFeedback, []string{"recalls"}, nil},
		{V2ToolGetDream, []string{"hypothesis_id"}, []string{"dream_id"}},
		{V2ToolResolveDreamFeedback, []string{"hypothesis_id", "decision", "reason", "evidence", "proposal"}, []string{"dream_id", "feedback"}},
		{V2ToolFindMemoryPackCandidates, []string{"query", "limit", "predicate_keys"}, nil},
		{V2ToolExportMemoryPack, []string{"include_evidence", "include_entity_names"}, []string{"include_support"}},
		{V2ToolInspectMemoryPack, []string{"artifact_json", "url", "expected_sha256", "mode"}, []string{"recommend_decisions"}},
		{V2ToolRollbackMemoryPackImport, []string{"import_id", "dry_run"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			props := schemaProperties(tools[tc.tool].InputSchema)
			for _, field := range tc.required {
				if _, ok := props[field]; !ok {
					t.Errorf("missing canonical field %s", field)
				}
			}
			for _, field := range tc.forbidden {
				if _, ok := props[field]; ok {
					t.Errorf("contains legacy field %s", field)
				}
			}
		})
	}
}

func TestV2CorrectionRequiresCanonicalDryRunAndApplyFields(t *testing.T) {
	tool, err := requireV2Tool(v2ToolMap(t), V2ToolCorrectEntityResolution)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"operation":             "split",
		"source_entity_id":      "ent-source",
		"target_entity_id":      nil,
		"owned_observation_ids": []any{"obs-1"},
		"evidence":              []any{map[string]any{"content": "These observations refer to a different person."}},
		"idempotency_key":       "split-1",
	}
	dryRun := mergeMap(base, map[string]any{"dry_run": true})
	if err := ValidateV2ContractInput(tool, dryRun, []string{"write"}); err != nil {
		t.Fatalf("dry run rejected: %v", err)
	}
	apply := mergeMap(base, map[string]any{"dry_run": false, "impact_token": "impact-1"})
	if err := ValidateV2ContractInput(tool, apply, []string{"write"}); err != nil {
		t.Fatalf("apply rejected: %v", err)
	}
	delete(apply, "impact_token")
	if err := ValidateV2ContractInput(tool, apply, []string{"write"}); err == nil || !strings.Contains(err.Error(), "impact_token is required") {
		t.Fatalf("apply without impact token err = %v", err)
	}
}

func TestV2OutputSchemasAreClosed(t *testing.T) {
	for _, tool := range V2ContractTools() {
		t.Run(tool.Name, func(t *testing.T) {
			assertV2ClosedObjectSchemas(t, tool.OutputSchema, tool.Name+" output")
		})
	}
}

func TestV2ProviderAndEmbeddingContracts(t *testing.T) {
	if err := assertV2ProviderProposalSchema(verifier.V2ProviderProposalSchema()); err != nil {
		t.Fatal(err)
	}
	if err := assertV2VerifierResponseSchema(verifier.V2VerifierResponseSchema()); err != nil {
		t.Fatal(err)
	}
	sourceKinds := embedding.V2EmbeddingSourceKinds()
	for _, want := range []string{"evidence", "search_document", "recall_query"} {
		if !slices.Contains(sourceKinds, want) {
			t.Fatalf("embedding source kinds missing %s: %#v", want, sourceKinds)
		}
	}
	if embedding.V2EmbeddingContractVersion == "" {
		t.Fatal("V2 embedding contract version is empty")
	}
}

func assertV2ClosedObjectSchemas(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	if schema["type"] == "object" {
		boundedMap, _ := schema["x-bounded-map"].(bool)
		if closed, ok := schema["additionalProperties"].(bool); !boundedMap && (!ok || closed) {
			t.Errorf("%s is not closed", path)
		}
	}
	for name, child := range schemaProperties(schema) {
		assertV2ClosedObjectSchemas(t, child, path+"."+name)
	}
	if items, ok := schema["items"].(map[string]any); ok {
		assertV2ClosedObjectSchemas(t, items, path+"[]")
	}
	if alternatives, ok := schema["oneOf"].([]any); ok {
		for i, raw := range alternatives {
			if alternative, ok := raw.(map[string]any); ok {
				assertV2ClosedObjectSchemas(t, alternative, path+fmt.Sprintf(".oneOf[%d]", i))
			}
		}
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeMap(left map[string]any, right map[string]any) map[string]any {
	out := cloneMap(left)
	for key, value := range right {
		out[key] = value
	}
	return out
}

func TestV2PredicateOptionsContract(t *testing.T) {
	schema := v2PredicateDefinitionSchema()
	props := schemaProperties(schema)
	for _, field := range []string{
		"predicate_key",
		"version",
		"relationship_kind",
		"current_cardinality",
		"lifecycle_state",
	} {
		if _, ok := props[field]; !ok {
			t.Fatalf("predicate definition schema missing %s", field)
		}
	}
	relationshipKind := props["relationship_kind"]
	if err := validateSchemaValue("relationship_kind", "state", relationshipKind); err != nil {
		t.Fatalf("relationship_kind state rejected: %v", err)
	}
	if err := validateSchemaValue("relationship_kind", "versioned", relationshipKind); err == nil {
		t.Fatal("relationship_kind accepted removed policy_family value")
	}
	cardinality := props["current_cardinality"]
	if err := validateSchemaValue("current_cardinality", "one", cardinality); err != nil {
		t.Fatalf("current_cardinality one rejected: %v", err)
	}
	if err := validateSchemaValue("current_cardinality", "latest", cardinality); err == nil {
		t.Fatal("current_cardinality accepted non-wiki value")
	}
}

func v2ToolMap(t *testing.T) map[string]Tool {
	t.Helper()
	tools := map[string]Tool{}
	for _, tool := range V2ContractTools() {
		tools[tool.Name] = tool
	}
	return tools
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

func assertV2VerifierResponseSchema(schema map[string]any) error {
	if !schemaDisallowsAdditionalProperties(schema) {
		return errors.New("verifier response schema is not closed")
	}
	props := schemaProperties(schema)
	requiredFields := schemaRequiredFields(schema)
	for _, required := range []string{"request_id", "security_signals", "entity_results", "relationship_results"} {
		if _, ok := props[required]; !ok {
			return fmt.Errorf("verifier response schema has no %s", required)
		}
		if !slices.Contains(requiredFields, required) {
			return fmt.Errorf("verifier response schema does not require %s", required)
		}
	}
	for _, forbidden := range []string{"tier", "status", "support_count", "predicate_definitions"} {
		if _, ok := props[forbidden]; ok {
			return fmt.Errorf("verifier response schema allows %s", forbidden)
		}
	}
	return nil
}

func readV2ContractFixtures(t *testing.T) []v2ContractFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/v2_contract_fixtures.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixtures []v2ContractFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no V2 contract fixtures")
	}
	return fixtures
}

type stubV2RememberService struct {
	req memoryservice.V2RememberRequest
}

func (s *stubV2RememberService) RememberV2(_ context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.req = req
	return &memoryservice.V2RememberResult{
		IngestID:          "ingest-v2",
		Status:            string(domain.V2PlacementRunQueued),
		CheckAfterSeconds: 60,
		StatusTool:        V2ToolGetMemoryPlacement,
		Items: []memoryservice.V2RememberItemResult{
			{
				ItemID:        "item-v2",
				EvidenceIndex: 0,
				Category:      string(domain.V2EvidenceProcessed),
				SearchState:   string(domain.V2SearchProjectionNotRequired),
			},
		},
		SearchState: string(domain.V2SearchProjectionNotRequired),
	}, nil
}

type stubV2RecallService struct {
	req memoryservice.V2RecallRequest
}

func (s *stubV2RecallService) RecallV2(_ context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
	s.req = req
	return &memoryservice.V2RecallResult{
		RecallID: "rec-v2",
		Results: []memoryservice.V2RecallResultItem{{
			EvidenceID:      "evidence-v2",
			RelationshipIDs: []string{"relationship-v2"},
			Rank:            1,
			Context:         "Dense-Mem uses PostgreSQL.",
		}},
		SearchState: string(domain.V2SearchProjectionCurrent),
	}, nil
}
