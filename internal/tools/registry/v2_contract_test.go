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
		V2ToolImportMemoryPack,
	} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("V2 contract missing tool %s", name)
		}
	}
}

func TestV2ContractExcludesStandaloneCommunityTool(t *testing.T) {
	for _, tool := range V2ContractTools() {
		if tool.Name == "list_communities" {
			t.Fatal("public V2 catalog exposes server-controlled community data")
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

func TestV2RelationshipProposalCorrectionTargetRequiresCompleteShape(t *testing.T) {
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	evidence := []any{
		map[string]any{"content": "Dense-Mem uses PostgreSQL."},
	}
	entities := []any{
		map[string]any{"ref": "entity-1", "name": "Dense-Mem"},
		map[string]any{"ref": "entity-2", "name": "PostgreSQL"},
	}
	baseProposal := map[string]any{
		"proposal_id": "rel-1",
		"subject_ref": "entity-1",
		"predicate":   "uses",
		"object_ref":  "entity-2",
		"evidence": []any{
			map[string]any{"evidence_index": 0, "start": 0, "end": 25},
		},
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
			proposal := cloneMap(baseProposal)
			proposal["correction_target"] = tc.target
			input := map[string]any{
				"evidence": evidence,
				"proposal": map[string]any{
					"entities":      entities,
					"relationships": []any{proposal},
				},
			}
			err := ValidateV2ContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), "correction_target") {
				t.Fatalf("ValidateV2ContractInput err = %v, want correction_target error", err)
			}
		})
	}
}

func TestV2RelationshipProposalValidityFieldsBelongToRelationship(t *testing.T) {
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	baseInput := map[string]any{
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL after July 2026."},
		},
		"proposal": map[string]any{
			"entities": []any{
				map[string]any{"ref": "entity-1", "name": "Dense-Mem"},
			},
			"relationships": []any{
				map[string]any{
					"proposal_id":    "rel-1",
					"subject_ref":    "entity-1",
					"predicate":      "uses",
					"object_value":   map[string]any{"type": "string", "value": "PostgreSQL"},
					"valid_from":     "2026-07-01T00:00:00Z",
					"valid_to":       "2026-12-31T00:00:00Z",
					"client_comment": "from release planning evidence",
					"evidence": []any{
						map[string]any{"evidence_index": 0, "start": 10, "end": 20},
					},
				},
			},
		},
	}
	if err := ValidateV2ContractInput(remember, baseInput, []string{"write"}); err != nil {
		t.Fatalf("relationship-level validity fields rejected: %v", err)
	}

	invertedInput := cloneMap(baseInput)
	invertedInput["proposal"] = map[string]any{
		"entities": []any{
			map[string]any{"ref": "entity-1", "name": "Dense-Mem"},
		},
		"relationships": []any{
			map[string]any{
				"proposal_id":  "rel-1",
				"subject_ref":  "entity-1",
				"predicate":    "uses",
				"object_value": map[string]any{"type": "string", "value": "PostgreSQL"},
				"valid_from":   "2026-12-31T00:00:00Z",
				"valid_to":     "2026-07-01T00:00:00Z",
				"evidence": []any{
					map[string]any{"evidence_index": 0, "start": 10, "end": 20},
				},
			},
		},
	}
	err = ValidateV2ContractInput(remember, invertedInput, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "valid_to") {
		t.Fatalf("ValidateV2ContractInput err = %v, want inverted validity rejection", err)
	}

	badInput := cloneMap(baseInput)
	badInput["proposal"] = map[string]any{
		"entities": []any{
			map[string]any{"ref": "entity-1", "name": "Dense-Mem"},
		},
		"relationships": []any{
			map[string]any{
				"proposal_id":  "rel-1",
				"subject_ref":  "entity-1",
				"predicate":    "uses",
				"object_value": map[string]any{"type": "string", "value": "PostgreSQL"},
				"evidence": []any{
					map[string]any{
						"evidence_index": 0,
						"start":          10,
						"end":            20,
						"valid_from":     "2026-07-01T00:00:00Z",
					},
				},
			},
		},
	}
	err = ValidateV2ContractInput(remember, badInput, []string{"write"})
	if err == nil {
		t.Fatal("evidence-level valid_from accepted")
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
		name    string
		action  string
		valid   map[string]any
		missing string
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
			action: string(domain.V2ResolveConfirmNewEntity),
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
			action: string(domain.V2ResolveSelectPredicate),
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
			action: string(domain.V2ResolveAccept),
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
			action: string(domain.V2ResolveReject),
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
			action: string(domain.V2ResolveCorrect),
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
			action: string(domain.V2ResolveReleaseQuarantine),
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
			missing := tc.missing
			delete(invalid, tc.missing)
			err := ValidateV2ContractInput(resolve, invalid, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), missing+" is required") {
				t.Fatalf("missing %s err = %v", missing, err)
			}
		})
	}
}

func TestV2ResolveMemoryPlacementRejectsLegacyNestedDecision(t *testing.T) {
	resolve, err := requireV2Tool(v2ToolMap(t), V2ToolResolveMemoryPlacement)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"action":                 string(domain.V2ResolveSelectEntity),
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
	err = ValidateV2ContractInput(resolve, input, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "decision") {
		t.Fatalf("legacy nested decision err = %v", err)
	}
}

func TestV2ResolveMemoryPlacementReviewActionsRequireItemVersion(t *testing.T) {
	resolve, err := requireV2Tool(v2ToolMap(t), V2ToolResolveMemoryPlacement)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []domain.V2ResolveAction{
		domain.V2ResolveSelectEntity,
		domain.V2ResolveConfirmNewEntity,
		domain.V2ResolveSelectPredicate,
		domain.V2ResolveAccept,
		domain.V2ResolveReject,
		domain.V2ResolveCorrect,
		domain.V2ResolveReleaseQuarantine,
	} {
		input := map[string]any{
			"action":            string(action),
			"idempotency_key":   "resolution-1",
			"ingest_id":         "ing-1",
			"placement_item_id": "item-1",
		}
		err := ValidateV2ContractInput(resolve, input, []string{"write"})
		if err == nil || !strings.Contains(err.Error(), "placement_item_version is required") {
			t.Fatalf("%s missing placement_item_version err = %v", action, err)
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
		{V2ToolResolveMemoryPlacement, []string{"placement_item_version", "entity_ref", "candidate_entity_id", "message"}, []string{"contract_version", "decision", "reason"}},
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

func TestV2PublicErrorSchemaContract(t *testing.T) {
	schema := V2PublicErrorSchema()
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
	for _, code := range []domain.V2PublicErrorCode{
		domain.V2ErrorInvalidContractVersion,
		domain.V2ErrorInvalidInput,
		domain.V2ErrorUnauthorizedScope,
		domain.V2ErrorWrongOwner,
		domain.V2ErrorConflict,
		domain.V2ErrorProviderUnavailable,
		domain.V2ErrorProviderMalformed,
		domain.V2ErrorDegraded,
	} {
		if !slices.Contains(enumValues, string(code)) {
			t.Fatalf("public error enum missing %s: %#v", code, enumValues)
		}
	}

	detailsSchema := schemaProperties(schema)["details"]
	if detailsSchema["maxProperties"] != v2MetadataMaxProperties || detailsSchema["x-max-depth"] != 4 {
		t.Fatalf("details schema is not bounded: %#v", detailsSchema)
	}
}

func TestV2RememberIngestIdempotencyKeyFromEvidence(t *testing.T) {
	single := v2RememberIngestIdempotencyKey([]memoryservice.V2RememberEvidenceInput{{
		IdempotencyKey: " eval:doc-alpha ",
	}})
	if single != "eval:doc-alpha" {
		t.Fatalf("single key = %q", single)
	}

	batch := v2RememberIngestIdempotencyKey([]memoryservice.V2RememberEvidenceInput{
		{IdempotencyKey: "eval:doc-alpha"},
		{IdempotencyKey: "eval:doc-beta"},
	})
	if !strings.HasPrefix(batch, "batch:") || len(batch) != len("batch:")+64 {
		t.Fatalf("batch key = %q", batch)
	}
	if batch != v2RememberIngestIdempotencyKey([]memoryservice.V2RememberEvidenceInput{
		{IdempotencyKey: "eval:doc-alpha"},
		{IdempotencyKey: "eval:doc-beta"},
	}) {
		t.Fatal("batch key is not deterministic")
	}
	if got := v2RememberIngestIdempotencyKey([]memoryservice.V2RememberEvidenceInput{
		{IdempotencyKey: "eval:doc-alpha"},
		{},
	}); got != "" {
		t.Fatalf("partial batch key = %q", got)
	}
}

func TestV2RememberRequestFromContractInputDerivesIngestIdempotency(t *testing.T) {
	req, err := v2RememberRequestFromContractInput(map[string]any{
		"contract_version": domain.V2ContractVersion,
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
	want := v2RememberIngestIdempotencyKey(req.Evidence)
	if req.IdempotencyKey != want || !strings.HasPrefix(req.IdempotencyKey, "batch:") {
		t.Fatalf("derived request key = %q, want %q", req.IdempotencyKey, want)
	}

	explicit, err := v2RememberRequestFromContractInput(map[string]any{
		"contract_version": domain.V2ContractVersion,
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
