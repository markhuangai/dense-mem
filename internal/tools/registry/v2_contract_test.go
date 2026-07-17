package registry

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
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
