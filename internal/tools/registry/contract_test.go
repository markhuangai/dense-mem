package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
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
	input := flatRelationshipForContent(strings.Repeat("a", memoryEntryMaxLength))
	if err := ValidateContractInput(remember, input, []string{"write"}); err != nil {
		t.Fatalf("boundary content rejected: %v", err)
	}
	oversized := flatRelationshipForContent(strings.Repeat("a", memoryEntryMaxLength+1))
	err = ValidateContractInput(remember, oversized, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "maximum length") {
		t.Fatalf("oversized content err = %v, want maximum length", err)
	}
}

func flatRelationshipForContent(content string) map[string]any {
	return map[string]any{
		"evidence": []any{
			map[string]any{"content": content},
		},
		"relationships": []any{map[string]any{
			"ref": "a-relationship",
			"subject": map[string]any{
				"name": "a", "entity_kind": "project",
				"span": map[string]any{"evidence_index": 0, "start": 0, "end": 1},
			},
			"predicate": map[string]any{
				"proposed_key": "uses", "surface": "a",
				"span": map[string]any{"evidence_index": 0, "start": 1, "end": 2},
			},
			"object": map[string]any{"entity": map[string]any{
				"name": "a", "entity_kind": "product",
				"span": map[string]any{"evidence_index": 0, "start": 2, "end": 3},
			}},
			"polarity": "+", "modality": "statement",
			"supports": []any{map[string]any{"evidence_index": 0, "start": 0, "end": len([]rune(content))}},
		}},
	}
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
		ToolGetSubmissionStatus,
		ToolCorrectRelationship,
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

func TestToolVisibleHidesDormantContractTools(t *testing.T) {
	if !ToolVisible(context.Background(), Tool{Name: "recall_memory"}, RuntimeToolPolicy{}) {
		t.Fatal("ordinary tools should remain visible")
	}
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	if ToolVisible(context.Background(), remember, RuntimeToolPolicy{}) {
		t.Fatal("dormant contract tool is visible before cutover")
	}
	remember.Visibility = "active"
	if !ToolVisible(context.Background(), remember, RuntimeToolPolicy{}) {
		t.Fatal("non-dormant contract tool should be visible to cutover wiring")
	}
}

func TestRememberRejectsMixedSourceRevisionBatch(t *testing.T) {
	tools := toolMap(t)
	remember, err := requireTool(tools, ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateContractInput(remember, withRequiredFlatRelationship(map[string]any{
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
	}), []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "revision fields must match") {
		t.Fatalf("ValidateContractInput err = %v, want mixed source revision rejection", err)
	}
}

func TestFlatRelationshipsRequireExactlyOneObject(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing object",
			mutate: func(input map[string]any) {
				delete(relationship(input)["object"].(map[string]any), "entity")
			},
		},
		{
			name: "both object forms",
			mutate: func(input map[string]any) {
				relationship(input)["object"].(map[string]any)["value"] = map[string]any{
					"type": "string", "value": "PostgreSQL", "surface": "PostgreSQL",
					"span": map[string]any{"evidence_index": 0, "start": 15, "end": 25},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			tc.mutate(input)
			err := ValidateContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), "object requires exactly one entity or value") {
				t.Fatalf("ValidateContractInput err = %v, want object choice error", err)
			}
		})
	}
}

func TestFlatRelationshipCorrectionTargetRequiresCompleteShape(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		target map[string]any
	}{
		{
			name: "missing relationship id",
			target: map[string]any{
				"expected_version": 1,
			},
		},
		{
			name: "missing expected version",
			target: map[string]any{
				"relationship_id": uuid.NewString(),
			},
		},
		{
			name: "zero expected version",
			target: map[string]any{
				"relationship_id":  uuid.NewString(),
				"expected_version": 0,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			relationship(input)["correction_target"] = tc.target
			err := ValidateContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), "correction_target") {
				t.Fatalf("ValidateContractInput err = %v, want correction_target error", err)
			}
		})
	}
}

func TestFlatRelationshipConflictContextRequiresCompleteShape(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		context map[string]any
	}{
		{
			name: "missing conflict id",
			context: map[string]any{
				"expected_version": 1,
			},
		},
		{
			name: "missing expected version",
			context: map[string]any{
				"conflict_id": uuid.NewString(),
			},
		},
		{
			name: "zero expected version",
			context: map[string]any{
				"conflict_id":      uuid.NewString(),
				"expected_version": 0,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			relationship(input)["conflict_context"] = tc.context
			err := ValidateContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), "conflict_context") {
				t.Fatalf("ValidateContractInput err = %v, want conflict_context error", err)
			}
		})
	}

	validInput := validFlatRelationshipSubmission()
	relationship(validInput)["conflict_context"] = map[string]any{
		"conflict_id":      uuid.NewString(),
		"expected_version": 1,
	}
	if err := ValidateContractInput(remember, validInput, []string{"write"}); err != nil {
		t.Fatalf("ValidateContractInput valid conflict_context err = %v", err)
	}
}

func TestFlatRelationshipValidityFieldsBelongToRelationship(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	baseInput := validFlatRelationshipSubmission()
	baseRelationship := relationship(baseInput)
	baseRelationship["valid_from"] = "2026-07-01T00:00:00Z"
	baseRelationship["valid_to"] = "2026-12-31T00:00:00Z"
	baseRelationship["client_comment"] = "from release planning evidence"
	if err := ValidateContractInput(remember, baseInput, []string{"write"}); err != nil {
		t.Fatalf("relationship-level validity fields rejected: %v", err)
	}

	invertedInput := validFlatRelationshipSubmission()
	invertedRelationship := relationship(invertedInput)
	invertedRelationship["valid_from"] = "2026-12-31T00:00:00Z"
	invertedRelationship["valid_to"] = "2026-07-01T00:00:00Z"
	err = ValidateContractInput(remember, invertedInput, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "valid_to") {
		t.Fatalf("ValidateContractInput err = %v, want inverted validity rejection", err)
	}

	badInput := validFlatRelationshipSubmission()
	relationship(badInput)["supports"] = []any{map[string]any{
		"evidence_index": 0,
		"start":          0,
		"end":            26,
		"valid_from":     "2026-07-01T00:00:00Z",
	}}
	err = ValidateContractInput(remember, badInput, []string{"write"})
	if err == nil {
		t.Fatal("evidence-level valid_from accepted")
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
	for _, required := range []string{"related_relationships", "related_communities", "related_hypotheses", "search_states", "degradations"} {
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
		{ToolRemember, []string{"evidence", "relationships"}, nil, []string{"contract_version", "entity_hints", "relationship_hints", "proposal"}},
		{ToolGetSubmissionStatus, []string{"submission_id"}, []string{"submission_id"}, []string{"ingest_id", "placement_item_id", "items", "review_tasks"}},
		{ToolCorrectRelationship, []string{"action", "relationship_id", "expected_version", "patch", "supports", "reason", "submission_id", "confirmation_token", "selection"}, []string{"action", "idempotency_key"}, []string{"operation", "source_entity_id", "target_entity_id", "owned_observation_ids", "dry_run", "impact_token", "evidence"}},
		{ToolRecallMemory, []string{"known_evidence_ids", "known_relationship_ids", "expand_from_entity_ids"}, nil, []string{"include_evidence", "use_communities"}},
		{ToolTraceMemory, []string{"include_verification", "include_transitions", "max_depth", "predicate_keys", "topic", "min_relevance"}, nil, []string{"max_chars"}},
		{ToolSubmitRecallSessionFeedback, []string{"recalls"}, nil, nil},
		{ToolGetDream, []string{"hypothesis_id"}, nil, []string{"dream_id"}},
		{ToolResolveDreamFeedback, []string{"hypothesis_id", "decision", "reason", "evidence", "relationships"}, nil, []string{"dream_id", "feedback", "proposal"}},
		{ToolExportMemoryPack, []string{"relationship_ids"}, []string{"name", "relationship_ids"}, []string{"include_support"}},
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

func TestCorrectionRequiresSubmitOrConfirmFields(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolCorrectRelationship)
	if err != nil {
		t.Fatal(err)
	}
	submit := map[string]any{
		"action":           "submit",
		"relationship_id":  "relationship-source",
		"expected_version": 2,
		"patch":            map[string]any{"predicate": map[string]any{"key": "works_with"}},
		"supports":         []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 8}},
		"reason":           "predicate resolved incorrectly",
		"idempotency_key":  "correction-submit-1",
	}
	if err := ValidateContractInput(tool, submit, []string{"write"}); err != nil {
		t.Fatalf("submit rejected: %v", err)
	}
	confirm := map[string]any{
		"action":             "confirm",
		"submission_id":      "correction-submission",
		"confirmation_token": "confirmation-token",
		"selection":          map[string]any{"subject_entity_id": "entity-selected"},
		"idempotency_key":    "correction-confirm-1",
	}
	if err := ValidateContractInput(tool, confirm, []string{"write"}); err != nil {
		t.Fatalf("confirm rejected: %v", err)
	}

	for _, test := range []struct {
		name      string
		input     map[string]any
		wantError string
	}{
		{
			name: "submit missing supports",
			input: map[string]any{
				"action": "submit", "relationship_id": "relationship-source", "expected_version": 2,
				"patch":  map[string]any{"predicate": map[string]any{"key": "works_with"}},
				"reason": "predicate resolved incorrectly", "idempotency_key": "correction-submit-missing",
			},
			wantError: "supports is required",
		},
		{
			name: "submit with confirmation field",
			input: map[string]any{
				"action": "submit", "relationship_id": "relationship-source", "expected_version": 2,
				"patch":    map[string]any{"predicate": map[string]any{"key": "works_with"}},
				"supports": []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 8}},
				"reason":   "predicate resolved incorrectly", "submission_id": "not-accepted",
				"idempotency_key": "correction-submit-mixed",
			},
			wantError: "submission_id is not accepted for action submit",
		},
		{
			name: "confirm with submit field",
			input: map[string]any{
				"action": "confirm", "submission_id": "correction-submission", "confirmation_token": "confirmation-token",
				"selection": map[string]any{"subject_entity_id": "entity-selected"}, "relationship_id": "not-accepted",
				"idempotency_key": "correction-confirm-mixed",
			},
			wantError: "relationship_id is not accepted for action confirm",
		},
		{
			name: "confirm with empty selection",
			input: map[string]any{
				"action": "confirm", "submission_id": "correction-submission", "confirmation_token": "confirmation-token",
				"selection": map[string]any{}, "idempotency_key": "correction-confirm-empty",
			},
			wantError: "selection must choose at least one Entity candidate",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateContractInput(tool, test.input, []string{"write"}); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v; want %q", err, test.wantError)
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

func TestProviderAndEmbeddingContracts(t *testing.T) {
	if err := assertProviderProposalSchema(verifier.ProviderProposalSchema()); err != nil {
		t.Fatal(err)
	}
	if err := assertVerifierResponseSchema(verifier.VerifierResponseSchema()); err != nil {
		t.Fatal(err)
	}
	sourceKinds := embedding.EmbeddingSourceKinds()
	for _, want := range []string{"evidence", "search_document", "recall_query"} {
		if !slices.Contains(sourceKinds, want) {
			t.Fatalf("embedding source kinds missing %s: %#v", want, sourceKinds)
		}
	}
	if embedding.EmbeddingContractVersion == "" {
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

func TestRememberIngestIdempotencyKeyFromEvidence(t *testing.T) {
	single := rememberIngestIdempotencyKey([]memoryservice.RememberEvidenceInput{{
		IdempotencyKey: " eval:doc-alpha ",
	}})
	if single != "eval:doc-alpha" {
		t.Fatalf("single key = %q", single)
	}

	batch := rememberIngestIdempotencyKey([]memoryservice.RememberEvidenceInput{
		{IdempotencyKey: "eval:doc-alpha"},
		{IdempotencyKey: "eval:doc-beta"},
	})
	if !strings.HasPrefix(batch, "batch:") || len(batch) != len("batch:")+64 {
		t.Fatalf("batch key = %q", batch)
	}
	if batch != rememberIngestIdempotencyKey([]memoryservice.RememberEvidenceInput{
		{IdempotencyKey: "eval:doc-alpha"},
		{IdempotencyKey: "eval:doc-beta"},
	}) {
		t.Fatal("batch key is not deterministic")
	}
	if got := rememberIngestIdempotencyKey([]memoryservice.RememberEvidenceInput{
		{IdempotencyKey: "eval:doc-alpha"},
		{},
	}); got != "" {
		t.Fatalf("partial batch key = %q", got)
	}
}

func TestRememberRequestFromContractInputDerivesIngestIdempotency(t *testing.T) {
	req, err := rememberRequestFromContractInput(map[string]any{
		"contract_version": domain.ContractVersion,
		"evidence": []any{
			map[string]any{
				"content":         "alpha evidence",
				"idempotency_key": " eval:doc-alpha ",
			},
			map[string]any{
				"content":         "beta evidence",
				"idempotency_key": "eval:doc-beta",
			},
		},
	})
	if err != nil {
		t.Fatalf("map request: %v", err)
	}
	want := rememberIngestIdempotencyKey(req.Evidence)
	if req.IdempotencyKey != want || !strings.HasPrefix(req.IdempotencyKey, "batch:") {
		t.Fatalf("derived request key = %q, want %q", req.IdempotencyKey, want)
	}

	explicit, err := rememberRequestFromContractInput(map[string]any{
		"contract_version": domain.ContractVersion,
		"idempotency_key":  "explicit-ingest-key",
		"evidence": []any{
			map[string]any{
				"content":         "alpha evidence",
				"idempotency_key": "eval:doc-alpha",
			},
		},
	})
	if err != nil {
		t.Fatalf("map explicit request: %v", err)
	}
	if explicit.IdempotencyKey != "explicit-ingest-key" {
		t.Fatalf("explicit request key = %q", explicit.IdempotencyKey)
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
