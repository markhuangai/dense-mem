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
	Name            string         `json:"name"`
	Tool            string         `json:"tool"`
	ContractVersion string         `json:"contract_version"`
	Scopes          []string       `json:"scopes"`
	Valid           bool           `json:"valid"`
	WantError       string         `json:"want_error"`
	Input           map[string]any `json:"input"`
	Output          map[string]any `json:"output"`
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
			if fixture.ContractVersion != "" {
				tool.ContractVersion = fixture.ContractVersion
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

func TestContractVersionIsMetadataNotPayload(t *testing.T) {
	for _, tool := range ContractTools() {
		props := schemaProperties(tool.InputSchema)
		if _, ok := props["contract_version"]; ok {
			t.Fatalf("%s exposes contract_version as a payload field", tool.Name)
		}
		if got := tool.InputSchema["x-contract-version"]; got != domain.ContractVersion {
			t.Fatalf("%s x-contract-version = %v", tool.Name, got)
		}
	}
}

func TestIsContractToolDoesNotTrustVersionBeforeValidation(t *testing.T) {
	tool := ContractTools()[0]
	tool.ContractVersion = "dense-mem.v2.0"
	if !IsContractTool(tool) {
		t.Fatal("wrong-version descriptor bypasses transport validation")
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
		if tool.ContractVersion != domain.ContractVersion {
			t.Fatalf("%s contract version = %q", tool.Name, tool.ContractVersion)
		}
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
		ToolResolveMemoryPlacement,
		ToolCorrectEntityResolution,
		ToolRecallMemory,
		ToolTraceMemory,
		ToolImportMemoryPack,
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
	if !ToolVisible(context.Background(), Tool{Name: "recall_memory"}, nil) {
		t.Fatal("ordinary tools should remain visible")
	}
	remember, err := requireTool(toolMap(t), ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	if ToolVisible(context.Background(), remember, nil) {
		t.Fatal("dormant contract tool is visible before cutover")
	}
	remember.Visibility = "active"
	if !ToolVisible(context.Background(), remember, nil) {
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

func TestResolveMemoryPlacementActionRequiredFields(t *testing.T) {
	resolve, err := requireTool(toolMap(t), ToolResolveMemoryPlacement)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"idempotency_key": "resolution-1"}
	evidence := []any{map[string]any{"content": "The user supplied authoritative resolution evidence."}}
	cases := []struct {
		name    string
		action  string
		valid   map[string]any
		missing string
	}{
		{
			name:    "acknowledge requires ingest",
			action:  string(domain.ResolveAcknowledge),
			valid:   map[string]any{"ingest_id": "ing-1"},
			missing: "ingest_id",
		},
		{
			name:   "select entity requires mention and candidate",
			action: string(domain.ResolveSelectEntity),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"entity_ref":             "person-1",
				"candidate_entity_id":    "ent-1",
			},
			missing: "candidate_entity_id",
		},
		{
			name:   "confirm new entity requires evidence",
			action: string(domain.ResolveConfirmNewEntity),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"entity_ref":             "person-1",
				"evidence":               evidence,
			},
			missing: "evidence",
		},
		{
			name:   "select predicate requires predicate version",
			action: string(domain.ResolveSelectPredicate),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"observation_id":         "obs-1",
				"predicate_key":          "works_on",
				"predicate_version":      float64(1),
			},
			missing: "predicate_version",
		},
		{
			name:   "accept requires evidence",
			action: string(domain.ResolveAccept),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"evidence":               evidence,
			},
			missing: "evidence",
		},
		{
			name:   "reject requires reason",
			action: string(domain.ResolveReject),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"message":                "not supported by the source",
			},
			missing: "message",
		},
		{
			name:   "correct requires evidence",
			action: string(domain.ResolveCorrect),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"evidence":               evidence,
			},
			missing: "evidence",
		},
		{
			name:   "release quarantine requires reason",
			action: string(domain.ResolveReleaseQuarantine),
			valid: map[string]any{
				"ingest_id":              "ing-1",
				"placement_item_id":      "item-1",
				"placement_item_version": float64(1),
				"message":                "manager reviewed quarantine signal",
			},
			missing: "message",
		},
		{
			name:   "forget requires relationship target",
			action: string(domain.ResolveForget),
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
			if err := ValidateContractInput(resolve, valid, []string{"write"}); err != nil {
				t.Fatalf("valid action rejected: %v", err)
			}

			invalid := cloneMap(valid)
			missing := tc.missing
			delete(invalid, tc.missing)
			err := ValidateContractInput(resolve, invalid, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), missing+" is required") {
				t.Fatalf("missing %s err = %v", missing, err)
			}
		})
	}
}

func TestResolveMemoryPlacementRejectsLegacyNestedDecision(t *testing.T) {
	resolve, err := requireTool(toolMap(t), ToolResolveMemoryPlacement)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"action":                 string(domain.ResolveSelectEntity),
		"idempotency_key":        "resolution-1",
		"ingest_id":              "ing-1",
		"placement_item_id":      "item-1",
		"placement_item_version": float64(1),
		"entity_ref":             "person-1",
		"candidate_entity_id":    "ent-1",
		"decision": map[string]any{
			"mention_ref": "person-1",
			"entity_id":   "ent-1",
		},
	}
	err = ValidateContractInput(resolve, input, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "decision") {
		t.Fatalf("legacy nested decision err = %v", err)
	}
}

func TestResolveMemoryPlacementReviewActionsRequireItemVersion(t *testing.T) {
	resolve, err := requireTool(toolMap(t), ToolResolveMemoryPlacement)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []domain.ResolveAction{
		domain.ResolveSelectEntity,
		domain.ResolveConfirmNewEntity,
		domain.ResolveSelectPredicate,
		domain.ResolveAccept,
		domain.ResolveReject,
		domain.ResolveCorrect,
		domain.ResolveReleaseQuarantine,
	} {
		input := map[string]any{
			"action":            string(action),
			"idempotency_key":   "resolution-1",
			"ingest_id":         "ing-1",
			"placement_item_id": "item-1",
		}
		err := ValidateContractInput(resolve, input, []string{"write"})
		if err == nil || !strings.Contains(err.Error(), "placement_item_version is required") {
			t.Fatalf("%s missing placement_item_version err = %v", action, err)
		}
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
		tool      string
		required  []string
		forbidden []string
	}{
		{ToolRemember, []string{"evidence", "relationships"}, []string{"contract_version", "entity_hints", "relationship_hints", "proposal"}},
		{ToolResolveMemoryPlacement, []string{"placement_item_version", "entity_ref", "candidate_entity_id", "message"}, []string{"contract_version", "decision", "reason"}},
		{ToolCorrectEntityResolution, []string{"operation", "owned_observation_ids", "impact_token"}, []string{"action", "selected_observation_ids", "plan_token"}},
		{ToolRecallMemory, []string{"known_evidence_ids", "known_relationship_ids", "expand_from_entity_ids"}, []string{"include_evidence", "use_communities"}},
		{ToolTraceMemory, []string{"include_verification", "include_transitions", "max_depth", "predicate_keys", "topic", "min_relevance"}, []string{"max_chars"}},
		{ToolSubmitRecallSessionFeedback, []string{"recalls"}, nil},
		{ToolGetDream, []string{"hypothesis_id"}, []string{"dream_id"}},
		{ToolResolveDreamFeedback, []string{"hypothesis_id", "decision", "reason", "evidence", "relationships"}, []string{"dream_id", "feedback", "proposal"}},
		{ToolFindMemoryPackCandidates, []string{"query", "limit", "predicate_keys"}, nil},
		{ToolExportMemoryPack, []string{"include_evidence", "include_entity_names"}, []string{"include_support"}},
		{ToolInspectMemoryPack, []string{"artifact_json", "url", "expected_sha256", "mode"}, []string{"recommend_decisions"}},
		{ToolRollbackMemoryPackImport, []string{"import_id", "dry_run"}, nil},
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

func TestCorrectionRequiresCanonicalDryRunAndApplyFields(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolCorrectEntityResolution)
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
	if err := ValidateContractInput(tool, dryRun, []string{"write"}); err != nil {
		t.Fatalf("dry run rejected: %v", err)
	}
	apply := mergeMap(base, map[string]any{"dry_run": false, "impact_token": "impact-1"})
	if err := ValidateContractInput(tool, apply, []string{"write"}); err != nil {
		t.Fatalf("apply rejected: %v", err)
	}
	delete(apply, "impact_token")
	if err := ValidateContractInput(tool, apply, []string{"write"}); err == nil || !strings.Contains(err.Error(), "impact_token is required") {
		t.Fatalf("apply without impact token err = %v", err)
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
		domain.ErrorInvalidContractVersion,
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

func TestPredicateOptionsContract(t *testing.T) {
	schema := predicateDefinitionSchema()
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

func TestPlacementReviewOptionSchemaRequiresOneCompleteOptionShape(t *testing.T) {
	schema := placementReviewOptionSchema()
	valid := []map[string]any{
		{"entity_id": "entity-1", "canonical_name": "Dense-Mem", "kind": "product"},
		{"predicate_key": "works_on", "version": 1, "aliases": []string{"works on"}},
		{"action": "submit_new_evidence"},
	}
	for _, option := range valid {
		if err := validateSchemaValue("option", option, schema); err != nil {
			t.Fatalf("validateSchemaValue(%#v) error = %v", option, err)
		}
	}
	for _, option := range []map[string]any{
		{},
		{"entity_id": "entity-1"},
		{"entity_id": "entity-1", "canonical_name": "Dense-Mem", "kind": "product", "action": "submit_new_evidence"},
	} {
		if err := validateSchemaValue("option", option, schema); err == nil {
			t.Fatalf("validateSchemaValue(%#v) accepted incomplete or mixed option", option)
		}
	}
	if got := placementReviewTaskArraySchema()["maxItems"]; got != 300 {
		t.Fatalf("review task maxItems = %#v, want 300", got)
	}
}
