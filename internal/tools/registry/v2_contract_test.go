package registry

import (
	"context"
	"encoding/json"
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
	Name      string         `json:"name"`
	Tool      string         `json:"tool"`
	Scopes    []string       `json:"scopes"`
	Valid     bool           `json:"valid"`
	WantError string         `json:"want_error"`
	Input     map[string]any `json:"input"`
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
			err = ValidateV2ContractInput(tool, fixture.Input, fixture.Scopes)
			if fixture.Valid {
				if err != nil {
					t.Fatalf("ValidateV2ContractInput: %v", err)
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
		"contract_version": domain.V2ContractVersion,
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
		"contract_version": domain.V2ContractVersion,
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
		"contract_version": domain.V2ContractVersion,
		"team_id":          "attacker-team",
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("remember.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestV2RememberRejectsMixedSourceRevisionBatch(t *testing.T) {
	tools := v2ToolMap(t)
	remember, err := requireV2Tool(tools, V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateV2ContractInput(remember, map[string]any{
		"contract_version": domain.V2ContractVersion,
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

func TestV2RelationshipHintsRequireExactlyOneObject(t *testing.T) {
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	baseInput := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL."},
		},
	}
	baseHint := map[string]any{
		"ref":         "rel-1",
		"subject_ref": "entity-1",
		"predicate":   "uses",
		"evidence": []any{
			map[string]any{"evidence_index": 0, "quote": "uses PostgreSQL"},
		},
	}

	cases := []struct {
		name string
		hint map[string]any
	}{
		{
			name: "missing object",
			hint: cloneMap(baseHint),
		},
		{
			name: "both object forms",
			hint: mergeMap(baseHint, map[string]any{
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
			input["relationship_hints"] = []any{tc.hint}
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
	base := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"idempotency_key":  "resolution-1",
	}
	evidence := []any{map[string]any{"content": "The user supplied authoritative resolution evidence."}}
	cases := []struct {
		name      string
		action    string
		valid     map[string]any
		missing   string
		wantError string
	}{
		{
			name:    "acknowledge requires ingest",
			action:  string(domain.V2ResolveAcknowledge),
			valid:   map[string]any{"ingest_id": "ing-1"},
			missing: "ingest_id",
		},
		{
			name:   "select entity requires target and candidate",
			action: string(domain.V2ResolveSelectEntity),
			valid: map[string]any{
				"ingest_id":           "ing-1",
				"placement_item_id":   "item-1",
				"entity_ref":          "person-1",
				"candidate_entity_id": "ent-1",
			},
			missing: "candidate_entity_id",
		},
		{
			name:   "confirm new entity requires evidence",
			action: string(domain.V2ResolveConfirmNewEntity),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"entity_ref":        "person-1",
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
				"observation_id":    "obs-1",
				"predicate_key":     "works_on",
				"predicate_version": float64(1),
			},
			missing: "predicate_version",
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
			name:   "reject requires message",
			action: string(domain.V2ResolveReject),
			valid: map[string]any{
				"ingest_id":         "ing-1",
				"placement_item_id": "item-1",
				"message":           "not supported by the source",
			},
			missing: "message",
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
				"message":           "manager reviewed quarantine signal",
			},
			missing: "message",
		},
		{
			name:   "forget requires relationship target",
			action: string(domain.V2ResolveForget),
			valid: map[string]any{
				"relationship_id": "rel-1",
				"message":         "user requested retraction",
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
			delete(invalid, tc.missing)
			err := ValidateV2ContractInput(resolve, invalid, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), tc.missing+" is required") {
				t.Fatalf("missing %s err = %v", tc.missing, err)
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
}

func TestV2ProviderAndEmbeddingContracts(t *testing.T) {
	if err := assertV2ProviderProposalSchema(verifier.V2ProviderProposalSchema()); err != nil {
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
				Category:      string(domain.V2EvidenceNeedsReview),
				SearchState:   string(domain.V2SearchProjectionNotRequired),
			},
		},
		SearchState: string(domain.V2SearchProjectionNotRequired),
	}, nil
}
