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
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
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
	expectedNames := V2ContractToolNames()
	if len(tools) != len(expectedNames) {
		t.Fatalf("tool count = %d, want %d", len(tools), len(expectedNames))
	}
	seen := map[string]struct{}{}
	for _, tool := range tools {
		if !slices.Contains(expectedNames, tool.Name) {
			t.Fatalf("unexpected V2 tool name %s", tool.Name)
		}
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
	for _, name := range expectedNames {
		if _, ok := seen[name]; !ok {
			t.Fatalf("V2 contract missing tool %s", name)
		}
	}
}

func TestV2RelationshipHintsRejectOutOfRangeEvidenceIndex(t *testing.T) {
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL."},
		},
		"relationship_hints": []any{
			map[string]any{
				"ref":         "rel-1",
				"subject_ref": "entity-1",
				"predicate":   "uses",
				"object_ref":  "entity-2",
				"evidence": []any{
					map[string]any{"evidence_index": 1, "quote": "uses PostgreSQL"},
				},
			},
		},
	}
	err = ValidateV2ContractInput(remember, input, []string{"write"})
	if err == nil || !strings.Contains(err.Error(), "outside evidence length") {
		t.Fatalf("ValidateV2ContractInput err = %v, want evidence length error", err)
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

func TestIsV2ContractTool(t *testing.T) {
	if !IsV2ContractTool(Tool{Name: V2ToolRemember, ContractVersion: domain.V2ContractVersion}) {
		t.Fatal("V2 contract tool was not recognized")
	}
	if IsV2ContractTool(Tool{Name: "remember", ContractVersion: ""}) {
		t.Fatal("legacy tool was recognized as V2")
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
		"contract_version": domain.V2ContractVersion,
		"query":            "PostgreSQL memory",
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
		"contract_version": domain.V2ContractVersion,
		"team_id":          "attacker-team",
		"query":            "PostgreSQL memory",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("recall_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATWiresExecutableTraceMemory(t *testing.T) {
	stub := &stubV2TraceContext{}
	reg, err := BuildV2UAT(Dependencies{Context: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	trace, ok := reg.Get(V2ToolTraceMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register trace_memory")
	}
	if trace.Invoke == nil {
		t.Fatal("BuildV2UAT trace_memory invoker is nil")
	}
	out, err := trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"contract_version":         domain.V2ContractVersion,
		"relationship_id":          "relationship-v2",
		"include_evidence_content": false,
		"include_verification":     true,
		"include_transitions":      false,
		"max_depth":                2,
		"max_edges":                12,
		"max_chars":                500,
		"predicate_keys":           []any{"works_on"},
		"topic":                    "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("trace_memory.Invoke: %v", err)
	}
	relationship, ok := out["relationship"].(map[string]any)
	if !ok || relationship["relationship_id"] != "relationship-v2" {
		t.Fatalf("relationship = %#v", out["relationship"])
	}
	if _, ok := out["v2_semantic"]; ok {
		t.Fatalf("trace_memory should unwrap V2 payload, got %#v", out)
	}
	if stub.req.RelationshipID != "relationship-v2" || stub.req.MaxDepth != 2 || stub.req.MaxEdges != 12 || stub.req.MaxChars != 500 {
		t.Fatalf("trace request = %#v", stub.req)
	}
	if got := strings.Join(stub.req.PredicateKeys, ","); got != "works_on" {
		t.Fatalf("predicate_keys = %q", got)
	}
}

func TestBuildV2UATTraceRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{Context: &stubV2TraceContext{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	trace, ok := reg.Get(V2ToolTraceMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register trace_memory")
	}
	_, err = trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"contract_version": domain.V2ContractVersion,
		"team_id":          "attacker-team",
		"relationship_id":  "relationship-v2",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("trace_memory.Invoke err = %v, want tenant override rejection", err)
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

func TestV2RelationshipHintsResolveEntityRefs(t *testing.T) {
	remember, err := requireV2Tool(v2ToolMap(t), V2ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	baseInput := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL."},
		},
		"entity_hints": []any{
			map[string]any{"ref": "entity-1", "name": "Dense-Mem"},
			map[string]any{"ref": "entity-2", "name": "PostgreSQL"},
		},
	}
	baseHint := map[string]any{
		"ref":         "rel-1",
		"subject_ref": "entity-1",
		"predicate":   "uses",
		"object_ref":  "entity-2",
		"evidence": []any{
			map[string]any{"evidence_index": 0, "quote": "uses PostgreSQL"},
		},
	}
	valid := cloneMap(baseInput)
	valid["relationship_hints"] = []any{baseHint}
	if err := ValidateV2ContractInput(remember, valid, []string{"write"}); err != nil {
		t.Fatalf("valid relationship refs rejected: %v", err)
	}
	objectValueHint := cloneMap(baseHint)
	delete(objectValueHint, "object_ref")
	objectValueHint["object_value"] = map[string]any{
		"type":  "string",
		"value": "PostgreSQL",
	}
	objectValue := cloneMap(baseInput)
	objectValue["relationship_hints"] = []any{objectValueHint}
	if err := ValidateV2ContractInput(remember, objectValue, []string{"write"}); err != nil {
		t.Fatalf("object_value relationship rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		hint map[string]any
		want string
	}{
		{
			name: "missing subject",
			hint: mergeMap(baseHint, map[string]any{"subject_ref": "entity-missing"}),
			want: "subject_ref",
		},
		{
			name: "missing object",
			hint: mergeMap(baseHint, map[string]any{"object_ref": "entity-missing"}),
			want: "object_ref",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := cloneMap(baseInput)
			input["relationship_hints"] = []any{tc.hint}
			err := ValidateV2ContractInput(remember, input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateV2ContractInput err = %v, want %q", err, tc.want)
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
			name:   "reject requires message",
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
			delete(invalid, tc.missing)
			err := ValidateV2ContractInput(resolve, invalid, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), tc.missing+" is required") {
				t.Fatalf("missing %s err = %v", tc.missing, err)
			}
		})
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
			"contract_version":  domain.V2ContractVersion,
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

func TestV2CorrectEntityResolutionActionRequiredFields(t *testing.T) {
	correct, err := requireV2Tool(v2ToolMap(t), V2ToolCorrectEntityResolution)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"source_entity_id": "ent-source",
		"dry_run":          true,
	}
	merge := mergeMap(base, map[string]any{
		"action":           string(domain.V2EntityCorrectionMerge),
		"target_entity_id": "ent-target",
	})
	if err := ValidateV2ContractInput(correct, merge, []string{"write"}); err != nil {
		t.Fatalf("valid merge rejected: %v", err)
	}
	split := mergeMap(base, map[string]any{
		"action":                   string(domain.V2EntityCorrectionSplit),
		"selected_observation_ids": []any{"obs-1"},
	})
	if err := ValidateV2ContractInput(correct, split, []string{"write"}); err != nil {
		t.Fatalf("valid split rejected: %v", err)
	}

	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name: "merge requires target",
			input: mergeMap(base, map[string]any{
				"action": string(domain.V2EntityCorrectionMerge),
			}),
			want: "target_entity_id is required",
		},
		{
			name: "split requires selected observations",
			input: mergeMap(base, map[string]any{
				"action": string(domain.V2EntityCorrectionSplit),
			}),
			want: "selected_observation_ids is required",
		},
		{
			name: "apply requires plan token",
			input: mergeMap(base, map[string]any{
				"action":           string(domain.V2EntityCorrectionMerge),
				"target_entity_id": "ent-target",
				"dry_run":          false,
			}),
			want: "plan_token is required",
		},
		{
			name: "split rejects target",
			input: mergeMap(split, map[string]any{
				"target_entity_id": "ent-target",
			}),
			want: "target_entity_id is only accepted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateV2ContractInput(correct, tc.input, []string{"write"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateV2ContractInput err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestV2ExportMemoryPackRequiresRelationships(t *testing.T) {
	exportTool, err := requireV2Tool(v2ToolMap(t), V2ToolExportMemoryPack)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"name":             "pack",
	}
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "missing relationships",
			input: cloneMap(base),
			want:  "relationship_ids is required",
		},
		{
			name:  "empty relationships",
			input: mergeMap(base, map[string]any{"relationship_ids": []any{}}),
			want:  "at least 1 items",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateV2ContractInput(exportTool, tc.input, []string{"read"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateV2ContractInput err = %v, want %q", err, tc.want)
			}
		})
	}
	valid := mergeMap(base, map[string]any{"relationship_ids": []any{"rel-1"}})
	if err := ValidateV2ContractInput(exportTool, valid, []string{"read"}); err != nil {
		t.Fatalf("valid export rejected: %v", err)
	}
}

func TestV2MemoryPackSourceValidation(t *testing.T) {
	tools := v2ToolMap(t)
	for _, toolName := range []string{V2ToolInspectMemoryPack, V2ToolImportMemoryPack} {
		tool, err := requireV2Tool(tools, toolName)
		if err != nil {
			t.Fatal(err)
		}
		base := map[string]any{"contract_version": domain.V2ContractVersion}
		if toolName == V2ToolImportMemoryPack {
			base["mode"] = "review"
		}
		validArtifact := mergeMap(base, map[string]any{"artifact_json": `{"items":[]}`})
		if err := ValidateV2ContractInput(tool, validArtifact, []string{"write", "read"}); err != nil {
			t.Fatalf("%s valid artifact rejected: %v", toolName, err)
		}
		validURL := mergeMap(base, map[string]any{"url": "https://example.com/pack.json"})
		if err := ValidateV2ContractInput(tool, validURL, []string{"write", "read"}); err != nil {
			t.Fatalf("%s valid URL rejected: %v", toolName, err)
		}
		for _, tc := range []struct {
			name  string
			input map[string]any
			want  string
		}{
			{"missing source", cloneMap(base), "exactly one"},
			{"both sources", mergeMap(base, map[string]any{"artifact_json": `{}`, "url": "https://example.com/pack.json"}), "exactly one"},
			{"non https URL", mergeMap(base, map[string]any{"url": "http://example.com/pack.json"}), "HTTPS"},
		} {
			t.Run(toolName+"/"+tc.name, func(t *testing.T) {
				err := ValidateV2ContractInput(tool, tc.input, []string{"write", "read"})
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("ValidateV2ContractInput err = %v, want %q", err, tc.want)
				}
			})
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
	schema := verifier.V2ProviderProposalSchema()
	if err := assertV2ProviderProposalSchema(schema); err != nil {
		t.Fatal(err)
	}
	reviewItems := schemaProperties(schema)["review_items"]
	reviewItem := reviewItems["items"].(map[string]any)
	category := schemaProperties(reviewItem)["category"]
	if err := validateSchemaValue("category", "relationship_needs_review", category); err != nil {
		t.Fatalf("provider review category rejected: %v", err)
	}
	for _, serverOwned := range []string{"relationship_fact", "relationship_validated_claim"} {
		if err := validateSchemaValue("category", serverOwned, category); err == nil {
			t.Fatalf("provider review category accepted server-owned outcome %s", serverOwned)
		}
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
	evidenceItem := schemaProperties(schemaProperties(schema)["evidence"]["items"].(map[string]any))
	if err := validateSchemaValue("evidence.content", "", evidenceItem["content"]); err == nil {
		t.Fatal("provider evidence content accepted empty string")
	}
	entityItem := schemaProperties(schemaProperties(schema)["entity_proposals"]["items"].(map[string]any))
	if err := validateSchemaValue("entity.name", "", entityItem["name"]); err == nil {
		t.Fatal("provider entity name accepted empty string")
	}
	if err := validateSchemaValue("entity.aliases", []any{""}, entityItem["aliases"]); err == nil {
		t.Fatal("provider entity alias accepted empty string")
	}
	relationshipItem := schemaProperties(schemaProperties(schema)["relationship_proposals"]["items"].(map[string]any))
	if err := validateSchemaValue("relationship.original_predicate", "", relationshipItem["original_predicate"]); err == nil {
		t.Fatal("provider relationship predicate accepted empty string")
	}
	objectValue := schemaProperties(relationshipItem["object_value"])
	if err := validateSchemaValue("relationship.object_value.value", "", objectValue["value"]); err == nil {
		t.Fatal("provider relationship value accepted empty string")
	}
	evidenceSpan := schemaProperties(relationshipItem["evidence"]["items"].(map[string]any))
	if err := validateSchemaValue("relationship.evidence.quote", "", evidenceSpan["quote"]); err == nil {
		t.Fatal("provider evidence quote accepted empty string")
	}
	reviewItemProps := schemaProperties(reviewItem)
	if err := validateSchemaValue("review.reason", "", reviewItemProps["reason"]); err == nil {
		t.Fatal("provider review reason accepted empty string")
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
	req          memoryservice.V2RememberRequest
	placementReq memoryservice.V2GetMemoryPlacementRequest
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
				Version:       1,
				EvidenceIndex: 0,
				Category:      string(domain.V2EvidenceNeedsReview),
				SearchState:   string(domain.V2SearchProjectionNotRequired),
			},
		},
		SearchState: string(domain.V2SearchProjectionNotRequired),
	}, nil
}

func (s *stubV2RememberService) GetMemoryPlacementV2(_ context.Context, req memoryservice.V2GetMemoryPlacementRequest) (*memoryservice.V2RememberResult, error) {
	s.placementReq = req
	return &memoryservice.V2RememberResult{
		IngestID:          req.IngestID,
		Status:            string(domain.V2PlacementRunCompleted),
		CheckAfterSeconds: 0,
		StatusTool:        V2ToolGetMemoryPlacement,
		Items: []memoryservice.V2RememberItemResult{{
			ItemID:        "item-v2",
			Version:       3,
			EvidenceIndex: 0,
			Category:      string(domain.V2EvidenceNeedsReview),
			SearchState:   string(domain.V2SearchProjectionNotRequired),
		}},
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

type stubV2TraceContext struct {
	req contextservice.TraceRequest
}

func (s *stubV2TraceContext) Trace(_ context.Context, _ string, req contextservice.TraceRequest) (*contextservice.TraceResult, error) {
	s.req = req
	return &contextservice.TraceResult{
		V2Semantic: &contextservice.V2SemanticTrace{
			Relationship: &repository.V2RelationshipTraceRecord{
				RelationshipID: "relationship-v2",
				PredicateKey:   "works_on",
			},
			EvidenceSupports: []repository.V2RelationshipEvidenceSupportRecord{{
				SupportID: "support-v2",
			}},
			StoppedReason: "max_edges",
			Truncated:     true,
		},
	}, nil
}

func (s *stubV2TraceContext) Assemble(context.Context, string, contextservice.AssembleRequest) (*contextservice.AssembleResult, error) {
	return nil, errors.New("not implemented")
}
