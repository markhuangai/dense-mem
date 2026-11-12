package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type contractFixture struct {
	Name      string         `json:"name"`
	Tool      string         `json:"tool"`
	Scopes    []string       `json:"scopes"`
	Valid     bool           `json:"valid"`
	WantError string         `json:"want_error"`
	Input     map[string]any `json:"input"`
	Output    map[string]any `json:"output"`
}

func TestContractFixtures(t *testing.T) {
	tools := toolMap(t)
	fixtures := readContractFixtures(t)

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			tool, err := requireTool(tools, fixture.Tool)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateContractInput(tool, fixture.Input, fixture.Scopes)
			if fixture.Valid {
				if err != nil {
					t.Fatalf("ValidateContractInput: %v", err)
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
				t.Fatal("ValidateContractInput succeeded, want error")
			}
			if fixture.WantError != "" && !strings.Contains(err.Error(), fixture.WantError) {
				t.Fatalf("error = %q, want containing %q", err.Error(), fixture.WantError)
			}
		})
	}
}

func TestContractRememberBoundaryContent(t *testing.T) {
	tools := toolMap(t)
	remember, err := requireTool(tools, ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	input := evidenceOnlyForContent(strings.Repeat("a", memoryEntryMaxLength))
	if err := ValidateContractInput(remember, input, []string{"read", "write"}); err != nil {
		t.Fatalf("boundary content rejected: %v", err)
	}
	oversized := evidenceOnlyForContent(strings.Repeat("a", memoryEntryMaxLength+1))
	err = ValidateContractInput(remember, oversized, []string{"read", "write"})
	if err == nil || !strings.Contains(err.Error(), "maximum length") {
		t.Fatalf("oversized content err = %v, want maximum length", err)
	}
}

func evidenceOnlyForContent(content string) map[string]any {
	return map[string]any{"idempotency_key": "remember-test-batch", "target_space": "team_shared", "evidence": []any{map[string]any{"content": content}}}
}

func TestContractSchemaHasNoPublicVersionMarker(t *testing.T) {
	for _, tool := range ContractTools() {
		props := schemaProperties(tool.InputSchema)
		if _, ok := props["contract_version"]; ok {
			t.Fatalf("%s exposes contract_version as a payload field", tool.Name)
		}
		if _, ok := tool.InputSchema["x-contract-version"]; ok {
			t.Fatalf("%s exposes x-contract-version metadata", tool.Name)
		}
	}
}

func TestPublicInputSchemasAvoidConverterHostileComposition(t *testing.T) {
	forbidden := map[string]struct{}{
		"oneOf": {}, "anyOf": {}, "allOf": {}, "not": {},
		"if": {}, "then": {}, "else": {},
	}
	for _, tool := range ContractTools() {
		t.Run(tool.Name, func(t *testing.T) {
			assertSchemaKeysAbsent(t, tool.InputSchema, tool.Name+" input", forbidden)
		})
	}
}

func TestContractCatalogMetadata(t *testing.T) {
	tools := ContractTools()
	if len(tools) != len(ContractToolNames()) {
		t.Fatalf("tool names length mismatch")
	}
	seen := map[string]struct{}{}
	for _, tool := range tools {
		if _, dup := seen[tool.Name]; dup {
			t.Fatalf("duplicate contract tool name %s", tool.Name)
		}
		seen[tool.Name] = struct{}{}
		if tool.FeatureGate != domain.FeatureGate || tool.Visibility != domain.ToolVisibility {
			t.Fatalf("%s gate metadata = %q/%q", tool.Name, tool.FeatureGate, tool.Visibility)
		}
		if len(tool.RequiredScopes) == 0 {
			t.Fatalf("%s has no required scopes", tool.Name)
		}
		if tool.Invoke != nil {
			t.Fatalf("%s has an invoker before the gate is wired", tool.Name)
		}
	}
	for _, name := range []string{
		ToolRemember,
		ToolIngestSession,
		ToolRetractEvidence,
		ToolRecallMemory,
		ToolTraceMemory,
		ToolExportMemoryPack,
	} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("contract missing tool %s", name)
		}
	}
}

func TestContractExcludesStandaloneCommunityTool(t *testing.T) {
	for _, tool := range ContractTools() {
		if tool.Name == "list_communities" {
			t.Fatal("public catalog exposes server-controlled community data")
		}
	}
}

func TestToolVisibleExposesCutoverContractTools(t *testing.T) {
	if !ToolVisible(context.Background(), Tool{Name: "recall_memory"}, RuntimeToolPolicy{}) {
		t.Fatal("ordinary tools should remain visible")
	}
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.New(), Kind: domain.MemorySpaceTeamShared, Generation: 1}}})
	if !ToolVisible(ctx, remember, RuntimeToolPolicy{}) {
		t.Fatal("cutover contract tool should be visible")
	}
}

func TestRememberRejectsMixedSourceRevisionBatch(t *testing.T) {
	tools := toolMap(t)
	remember, err := requireTool(tools, ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateContractInput(remember, withEvidenceOnlyRequest(map[string]any{
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
	}), []string{"read", "write"})
	if err == nil || !strings.Contains(err.Error(), "revision fields must match") {
		t.Fatalf("ValidateContractInput err = %v, want mixed source revision rejection", err)
	}
}

func TestRememberRejectsMixedSourceRevisionProvenance(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	base := []any{
		map[string]any{
			"content": "first source fragment", "source_key": "wiki://write-pipeline", "source_revision": "rev-1",
			"source_type": "document", "authority": "primary", "source": "wiki", "metadata": map[string]any{"section": "one"},
		},
		map[string]any{
			"content": "second source fragment", "source_key": "wiki://write-pipeline", "source_revision": "rev-1",
			"source_type": "document", "authority": "primary", "source": "wiki", "metadata": map[string]any{"section": "one"},
		},
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "source type", mutate: func(item map[string]any) { item["source_type"] = "manual" }},
		{name: "authority", mutate: func(item map[string]any) { item["authority"] = "secondary" }},
		{name: "metadata", mutate: func(item map[string]any) { item["metadata"] = map[string]any{"section": "two"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := []any{cloneMap(base[0].(map[string]any)), cloneMap(base[1].(map[string]any))}
			test.mutate(evidence[1].(map[string]any))

			err := ValidateContractInput(remember, withEvidenceOnlyRequest(map[string]any{"evidence": evidence}), []string{"read", "write"})

			require.ErrorContains(t, err, "including source provenance")
		})
	}
}

func TestDefaultRecallDoesNotPermitCandidatesOrHypotheses(t *testing.T) {
	tools := toolMap(t)
	recall, err := requireTool(tools, ToolRecallMemory)
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
	for _, required := range []string{"related_topics", "topics_truncated", "search_states", "degradations"} {
		if _, ok := props[required]; !ok {
			t.Fatalf("recall output missing %s", required)
		}
	}
	for _, removed := range []string{"discovery_paths", "discovery_guidance"} {
		if _, ok := props[removed]; ok {
			t.Fatalf("recall output exposes removed field %s", removed)
		}
	}
	for _, internal := range []string{"search_state", "degradation"} {
		if _, ok := props[internal]; ok {
			t.Fatalf("recall output exposes internal field %s", internal)
		}
	}
}

func TestCanonicalInputFieldNames(t *testing.T) {
	tools := toolMap(t)
	cases := []struct {
		tool           string
		canonical      []string
		schemaRequired []string
		forbidden      []string
	}{
		{ToolRemember, []string{"evidence"}, nil, []string{"contract_version", "entities", "relationships", "entity_hints", "relationship_hints", "proposal"}},
		{ToolRecallMemory, []string{"known_evidence_ids", "topic_ids", "topic_limit"}, nil, []string{"include_evidence", "use_communities"}},
		{ToolTraceMemory, []string{"evidence_id", "include_transitions", "include_evidence_content", "max_events", "max_fragment_content_runes"}, nil, []string{"max_chars"}},
		{ToolSubmitRecallSessionFeedback, []string{"recalls"}, nil, nil},
		{ToolExportMemoryPack, []string{"evidence_ids"}, []string{"name", "evidence_ids"}, []string{"include_support"}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			props := schemaProperties(tools[tc.tool].InputSchema)
			required := schemaRequiredFields(tools[tc.tool].InputSchema)
			for _, field := range tc.canonical {
				if _, ok := props[field]; !ok {
					t.Errorf("missing canonical field %s", field)
				}
			}
			for _, field := range tc.schemaRequired {
				if !slices.Contains(required, field) {
					t.Errorf("canonical field %s is not required by the schema", field)
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

func TestOutputSchemasAreClosed(t *testing.T) {
	for _, tool := range ContractTools() {
		t.Run(tool.Name, func(t *testing.T) {
			assertClosedObjectSchemas(t, tool.OutputSchema, tool.Name+" output")
		})
	}
}

func TestRememberEvidenceErrorsUseTheClosedCodeEnum(t *testing.T) {
	schema := rememberOutputSchema()
	expectedCodes := append([]string(nil), rememberapp.SubmissionErrorCodes()...)
	slices.Sort(expectedCodes)
	assertExactCodeEnum := func(label string, codeSchema map[string]any) {
		t.Helper()
		actualCodes, ok := codeSchema["enum"].([]string)
		if !ok {
			t.Fatalf("%s code enum = %#v", label, codeSchema["enum"])
		}
		actualCodes = append([]string(nil), actualCodes...)
		slices.Sort(actualCodes)
		if !slices.Equal(actualCodes, expectedCodes) {
			t.Fatalf("%s code enum = %#v, want exactly %#v", label, actualCodes, expectedCodes)
		}
	}
	evidence := schemaProperties(schema)["evidence"]
	evidenceItem := evidence["items"].(map[string]any)
	errorSchema := schemaProperties(evidenceItem)["error"]
	variants := errorSchema["oneOf"].([]any)
	errorObject := variants[1].(map[string]any)
	assertExactCodeEnum("evidence", schemaProperties(errorObject)["code"])
	topLevelErrors := schemaProperties(schema)["errors"]["items"].(map[string]any)
	assertExactCodeEnum("top-level", schemaProperties(topLevelErrors)["code"])
	require.Equal(t,
		[]string{"code", "message", "next_action", "remediation", "retryable"},
		sortedStrings(topLevelErrors["required"]),
	)
	nextActions, ok := schemaProperties(topLevelErrors)["next_action"]["enum"].([]string)
	require.True(t, ok)
	require.ElementsMatch(t, rememberapp.SubmissionNextActions(), nextActions)
}

func sortedStrings(raw any) []string {
	values, _ := raw.([]string)
	values = append([]string(nil), values...)
	slices.Sort(values)
	return values
}

func TestProviderAndEmbeddingContracts(t *testing.T) {
	sourceKinds := embeddingcontract.EmbeddingSourceKinds()
	for _, want := range []string{"evidence", "search_document", "recall_query"} {
		if !slices.Contains(sourceKinds, want) {
			t.Fatalf("embedding source kinds missing %s: %#v", want, sourceKinds)
		}
	}
	if embeddingcontract.EmbeddingContractVersion == "" {
		t.Fatal("embedding contract version is empty")
	}
}

func TestPublicErrorSchemaContract(t *testing.T) {
	schema := PublicErrorSchema()
	if !schemaDisallowsAdditionalProperties(schema) {
		t.Fatal("public error schema must be closed")
	}

	required := schemaRequiredFields(schema)
	for _, field := range []string{"code", "message", "retryable", "correlation_id"} {
		if !slices.Contains(required, field) {
			t.Fatalf("required fields missing %s: %#v", field, required)
		}
	}

	codeSchema := schemaProperties(schema)["code"]
	enumValues, ok := codeSchema["enum"].([]string)
	if !ok {
		t.Fatalf("code enum = %#v", codeSchema["enum"])
	}
	for _, code := range []domain.PublicErrorCode{
		domain.ErrorInvalidInput,
		domain.ErrorUnauthorizedScope,
		domain.ErrorWrongOwner,
		domain.ErrorConflict,
		domain.ErrorProviderUnavailable,
		domain.ErrorProviderMalformed,
		domain.ErrorDegraded,
	} {
		if !slices.Contains(enumValues, string(code)) {
			t.Fatalf("public error enum missing %s: %#v", code, enumValues)
		}
	}

	detailsSchema := schemaProperties(schema)["details"]
	if detailsSchema["maxProperties"] != metadataMaxProperties || detailsSchema["x-max-depth"] != 4 {
		t.Fatalf("details schema is not bounded: %#v", detailsSchema)
	}
}

func TestActionableOutputSchemaAcceptsPublicErrorsWithCollidingSuccessFields(t *testing.T) {
	feedback := toolMap(t)[ToolSubmitRecallSessionFeedback]
	nextActionSchema := schemaProperties(feedback.OutputSchema)["next_action"]
	if _, ok := nextActionSchema["anyOf"]; !ok {
		t.Fatalf("submit_recall_session_feedback next_action schema = %#v, want a property union", nextActionSchema)
	}
	publicError := map[string]any{
		"code":           string(domain.ErrorInvalidInput),
		"message":        "invalid input",
		"retryable":      false,
		"next_action":    "correct_and_resubmit",
		"remediation":    "Correct the request and submit again.",
		"correlation_id": uuid.NewString(),
	}
	if err := ValidateInput(Tool{InputSchema: feedback.OutputSchema}, publicError); err != nil {
		t.Fatalf("public error rejected by feedback output schema: %v", err)
	}
}

func assertClosedObjectSchemas(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	if schema["type"] == "object" {
		boundedMap, _ := schema["x-bounded-map"].(bool)
		if closed, ok := schema["additionalProperties"].(bool); !boundedMap && (!ok || closed) {
			t.Errorf("%s is not closed", path)
		}
	}
	for name, child := range schemaProperties(schema) {
		assertClosedObjectSchemas(t, child, path+"."+name)
	}
	if items, ok := schema["items"].(map[string]any); ok {
		assertClosedObjectSchemas(t, items, path+"[]")
	}
	if alternatives, ok := schema["oneOf"].([]any); ok {
		for i, raw := range alternatives {
			if alternative, ok := raw.(map[string]any); ok {
				assertClosedObjectSchemas(t, alternative, path+fmt.Sprintf(".oneOf[%d]", i))
			}
		}
	}
}

func assertSchemaKeysAbsent(t *testing.T, value any, path string, forbidden map[string]struct{}) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, blocked := forbidden[key]; blocked {
				t.Errorf("%s uses unsupported schema keyword %s", path, key)
			}
			assertSchemaKeysAbsent(t, child, path+"."+key, forbidden)
		}
	case []any:
		for index, child := range typed {
			assertSchemaKeysAbsent(t, child, fmt.Sprintf("%s[%d]", path, index), forbidden)
		}
	case []map[string]any:
		for index, child := range typed {
			assertSchemaKeysAbsent(t, child, fmt.Sprintf("%s[%d]", path, index), forbidden)
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
