package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- knowledge pipeline tests ---

func TestBuildDefaultExposesV2MemoryBoundaryTools(t *testing.T) {
	reg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	required := []string{
		"remember", "get_memory_placement", "resolve_memory_placement",
		"recall_memory", "trace_memory",
	}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
	removed := []string{
		"post_claim", "get_claim", "list_claims", "verify_claim",
		"promote_claim", "get_fact", "list_facts",
		"retract_fact", "retract_fragment", "detect_community", "get_community_summary", "list_communities",
		"keyword_search", "semantic_search", "graph_query", "list_recent_memories", "assemble_context",
		"dispute_memory_placement", "reflect_memories", "confirm_memory", "import_memories",
	}
	for _, name := range removed {
		if _, ok := reg.Get(name); ok {
			t.Errorf("removed client tool %q is still registered", name)
		}
	}
}

func TestBuildDefaultKnowledgeTools_ReturnUnavailableWhenDepsMissing(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"remember", map[string]any{"evidence": []any{map[string]any{"content": "hello"}}}},
		{"get_memory_placement", map[string]any{"ingest_id": "ingest-1"}},
		{"resolve_memory_placement", map[string]any{"ingest_id": "ingest-1", "message": "more evidence"}},
		{"find_memory_pack_candidates", map[string]any{"query": "react testing"}},
		{"export_memory_pack", map[string]any{"name": "React testing"}},
		{"inspect_memory_pack", map[string]any{"artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"x","items":[{"subject":"assistant","predicate":"has_skill","object":"x","source_kind":"manual"}]}`}},
		{"import_memory_pack", map[string]any{"mode": "review", "artifact_json": `{"schema_version":"dense-mem.memory_pack.v1","name":"x","items":[{"subject":"assistant","predicate":"has_skill","object":"x","source_kind":"manual"}]}`}},
		{"rollback_memory_pack_import", map[string]any{"import_id": "import-1"}},
	}
	for _, tc := range cases {
		tool, _ := reg.Get(tc.name)
		if _, err := tool.Invoke(context.Background(), "profileA", tc.input); !errors.Is(err, ErrToolUnavailable) {
			t.Errorf("%s err = %v; want ErrToolUnavailable", tc.name, err)
		}
	}
}

func TestBuildDefaultMemoryTools_InvokeAndScope(t *testing.T) {
	mem := &stubMemory{}
	reg, _ := BuildDefault(Dependencies{Memory: mem})

	cases := []struct {
		name  string
		input map[string]any
		scope string
	}{
		{"remember", map[string]any{"evidence": []any{map[string]any{"content": "hello"}}}, "write"},
		{"get_memory_placement", map[string]any{"ingest_id": "ingest-1"}, "read"},
		{"resolve_memory_placement", map[string]any{"ingest_id": "ingest-1", "message": "more evidence"}, "write"},
		{"resolve_memory_placement", map[string]any{"ingest_id": "ingest-1", "evidence": []any{map[string]any{"content": "corrected evidence"}}}, "write"},
	}
	for _, tc := range cases {
		tool, ok := reg.Get(tc.name)
		if !ok {
			t.Fatalf("%s not registered", tc.name)
		}
		if len(tool.RequiredScopes) != 1 || tool.RequiredScopes[0] != tc.scope {
			t.Fatalf("%s scopes = %v; want [%s]", tc.name, tool.RequiredScopes, tc.scope)
		}
		if _, err := tool.Invoke(context.Background(), "profile-memory", tc.input); err != nil {
			t.Fatalf("%s Invoke: %v", tc.name, err)
		}
		if mem.lastProfile != "profile-memory" {
			t.Fatalf("%s routed to %q; want profile-memory", tc.name, mem.lastProfile)
		}
	}

	tool, _ := reg.Get("resolve_memory_placement")
	_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{
		"ingest_id":         "ingest-1",
		"placement_item_id": "item-1",
		"action":            "release_quarantine",
		"message":           "Reviewed and approved guarded processing.",
	})
	if err != nil {
		t.Fatalf("resolve_memory_placement release_quarantine Invoke: %v", err)
	}
	if mem.lastDispute.Action != "release_quarantine" || mem.lastDispute.PlacementItemID != "item-1" {
		t.Fatalf("release request = %#v", mem.lastDispute)
	}
}

func TestRememberRejectsLegacyContentAndPromotionHints(t *testing.T) {
	mem := &stubMemory{}
	reg, _ := BuildDefault(Dependencies{Memory: mem})
	tool, ok := reg.Get("remember")
	if !ok {
		t.Fatal("remember not registered")
	}

	for _, tc := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{name: "legacy content", input: map[string]any{"content": "legacy content"}, want: "evidence is required"},
		{
			name: "client claims",
			input: map[string]any{
				"evidence": []any{map[string]any{"content": "The user likes Go."}},
				"claims":   []any{},
			},
			want: "unknown field: claims",
		},
		{
			name: "auto promote",
			input: map[string]any{
				"evidence":     []any{map[string]any{"content": "The user likes Go."}},
				"auto_promote": true,
			},
			want: "unknown field: auto_promote",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized := NormalizeInput(tool, tc.input)
			if _, ok := normalized["evidence"]; ok && tc.name == "legacy content" {
				t.Fatal("legacy content must not be rewritten into evidence")
			}
			if err := ValidateInput(tool, normalized); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateInput error = %v; want %q", err, tc.want)
			}
			if _, err := tool.Invoke(context.Background(), "profile-memory", tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Invoke error = %v; want %q", err, tc.want)
			}
			if len(mem.lastRemember.Evidence) != 0 {
				t.Fatalf("remember reached service with evidence = %#v", mem.lastRemember.Evidence)
			}
		})
	}
}

func TestBuildDefaultMemoryTools_InvalidInputBranches(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Memory: &stubMemory{}})

	for _, tc := range []struct {
		name string
		in   map[string]any
		want string
	}{
		{name: "remember", in: map[string]any{"evidence": func() {}}, want: "remember: invalid input"},
		{name: "get_memory_placement", in: map[string]any{"ingest_id": func() {}}, want: "get_memory_placement: invalid input"},
		{name: "resolve_memory_placement", in: map[string]any{"message": func() {}}, want: "resolve_memory_placement: invalid input"},
		{name: "resolve_memory_placement", in: map[string]any{}, want: "resolve_memory_placement: ingest_id or dispute_id is required"},
		{name: "resolve_memory_placement", in: map[string]any{"ingest_id": "ingest-1"}, want: "resolve_memory_placement: message or evidence is required"},
		{
			name: "resolve_memory_placement",
			in:   map[string]any{"ingest_id": "ingest-1", "action": "release_quarantine", "message": "reviewed"},
			want: "placement_item_id is required for release_quarantine",
		},
		{
			name: "resolve_memory_placement",
			in: map[string]any{
				"ingest_id":         "ingest-1",
				"placement_item_id": "item-1",
				"action":            "release_quarantine",
				"evidence":          []any{map[string]any{"content": "ignored"}},
			},
			want: "quarantine release reason is required",
		},
		{
			name: "resolve_memory_placement",
			in: map[string]any{
				"ingest_id":         "ingest-1",
				"placement_item_id": "item-1",
				"action":            "release_quarantine",
				"message":           "reviewed",
				"evidence":          []any{map[string]any{"content": "ignored"}},
			},
			want: "evidence is not accepted for release_quarantine",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "profile-memory", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
			}
		})
	}

	if _, ok := reg.Get("confirm_memory"); ok {
		t.Fatal("confirm_memory must not be registered in the v2 public tool surface")
	}
	if _, ok := reg.Get("reflect_memories"); ok {
		t.Fatal("reflect_memories must not be registered in the v2 public tool surface")
	}
	if _, ok := reg.Get("import_memories"); ok {
		t.Fatal("import_memories must not be registered in the v2 public tool surface")
	}
}

func TestBuildDefaultMemoryTools_GranularEntryValidation(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Memory: &stubMemory{}})

	remember, ok := reg.Get("remember")
	if !ok {
		t.Fatal("remember not registered")
	}
	properties := remember.InputSchema["properties"].(map[string]any)
	evidenceSchema := properties["evidence"].(map[string]any)
	itemSchema := evidenceSchema["items"].(map[string]any)
	itemProperties := itemSchema["properties"].(map[string]any)
	contentSchema := itemProperties["content"].(map[string]any)
	if got, want := contentSchema["maxLength"], memoryEntryMaxLength; got != want {
		t.Fatalf("remember.evidence[].content maxLength = %v; want %d", got, want)
	}
	err := ValidateInput(remember, map[string]any{
		"evidence": []any{map[string]any{"content": strings.Repeat("x", memoryEntryMaxLength+1)}},
	})
	if err == nil || !strings.Contains(err.Error(), "Store one coherent evidence item") {
		t.Fatalf("ValidateInput long remember evidence error = %v; want split guidance", err)
	}
	if err := ValidateInput(remember, map[string]any{
		"evidence": []any{map[string]any{"content": strings.Repeat("界", memoryEntryMaxLength)}},
	}); err != nil {
		t.Fatalf("ValidateInput non-ASCII exact max remember evidence: %v", err)
	}

	resolve, ok := reg.Get("resolve_memory_placement")
	if !ok {
		t.Fatal("resolve_memory_placement not registered")
	}
	resolveProperties := resolve.InputSchema["properties"].(map[string]any)
	actionSchema := resolveProperties["action"].(map[string]any)
	actionEnum, ok := actionSchema["enum"].([]string)
	if !ok || len(actionEnum) != 1 || actionEnum[0] != "release_quarantine" {
		t.Fatalf("resolve_memory_placement action enum = %#v", actionSchema["enum"])
	}
}

func TestBuildDefault_RecallInvokerErrorBranches(t *testing.T) {
	t.Run("empty hit returns mapping error", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{Recall: stubRecallNilFragment{}})
		tool, _ := reg.Get("recall_memory")

		_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{"query": "hello"})

		if err == nil || !strings.Contains(err.Error(), "hit missing payload") {
			t.Fatalf("err = %v; want hit missing payload", err)
		}
	})

	t.Run("memory reflection is not part of recall", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{Recall: stubRecall{}, Memory: stubMemoryReflectError{}})
		tool, _ := reg.Get("recall_memory")

		_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{"query": "hello"})

		if err != nil {
			t.Fatalf("err = %v; want nil", err)
		}
	})
}
