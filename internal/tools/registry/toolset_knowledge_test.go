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
		"remember", "get_memory_placement", "dispute_memory_placement",
		"recall_memory", "assemble_context", "trace_memory",
		"import_memories", "reflect_memories", "confirm_memory",
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
		"keyword_search", "semantic_search", "graph_query", "list_recent_memories",
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
		{"dispute_memory_placement", map[string]any{"ingest_id": "ingest-1", "message": "more evidence"}},
		{"import_memories", map[string]any{"summary": "hello"}},
		{"reflect_memories", map[string]any{}},
		{"confirm_memory", map[string]any{"claim_id": "claim-1", "decision": "keep_existing"}},
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
		{"dispute_memory_placement", map[string]any{"ingest_id": "ingest-1", "message": "more evidence"}, "write"},
		{"import_memories", map[string]any{"summary": "old chats"}, "write"},
		{"reflect_memories", map[string]any{}, "write"},
		{"confirm_memory", map[string]any{"claim_id": "claim-1", "decision": "keep_existing"}, "write"},
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
}

func TestRememberNormalizesLegacyContentWithoutPromotionHints(t *testing.T) {
	mem := &stubMemory{}
	reg, _ := BuildDefault(Dependencies{Memory: mem})
	tool, ok := reg.Get("remember")
	if !ok {
		t.Fatal("remember not registered")
	}
	legacy := map[string]any{
		"content":         "legacy content should be stored as evidence",
		"source":          "chat: compatibility",
		"idempotency_key": "legacy-remember-content",
		"labels":          []any{"decision", "security"},
		"metadata":        map[string]any{"origin": "legacy-client"},
		"auto_promote":    true,
		"claims": []any{map[string]any{
			"subject":         "client",
			"predicate":       "uses",
			"object":          "old remember shape",
			"extract_conf":    0.99,
			"resolution_conf": 0.99,
		}},
	}

	normalized := NormalizeInput(tool, legacy)
	if _, ok := normalized["claims"]; ok {
		t.Fatal("legacy claims must not survive remember normalization")
	}
	if _, ok := normalized["auto_promote"]; ok {
		t.Fatal("legacy auto_promote must not survive remember normalization")
	}
	if err := ValidateInput(tool, normalized); err != nil {
		t.Fatalf("ValidateInput normalized legacy remember: %v", err)
	}
	if _, err := tool.Invoke(context.Background(), "profile-memory", legacy); err != nil {
		t.Fatalf("legacy remember Invoke: %v", err)
	}
	if len(mem.lastRemember.Evidence) != 1 {
		t.Fatalf("remember evidence count = %d; want 1", len(mem.lastRemember.Evidence))
	}
	evidence := mem.lastRemember.Evidence[0]
	if evidence.Content != "legacy content should be stored as evidence" {
		t.Fatalf("remember evidence content = %q", evidence.Content)
	}
	if evidence.Source != "chat: compatibility" || evidence.IdempotencyKey != "legacy-remember-content" {
		t.Fatalf("remember evidence provenance = %#v", evidence)
	}
	if len(evidence.Labels) != 2 || evidence.Labels[0] != "decision" || evidence.Labels[1] != "security" {
		t.Fatalf("remember evidence labels = %#v", evidence.Labels)
	}
	if evidence.Metadata["origin"] != "legacy-client" {
		t.Fatalf("remember evidence metadata = %#v", evidence.Metadata)
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
		{name: "dispute_memory_placement", in: map[string]any{"message": func() {}}, want: "dispute_memory_placement: invalid input"},
		{name: "dispute_memory_placement", in: map[string]any{}, want: "dispute_memory_placement: ingest_id or dispute_id is required"},
		{name: "dispute_memory_placement", in: map[string]any{"ingest_id": "ingest-1"}, want: "dispute_memory_placement: message or evidence is required"},
		{name: "import_memories", in: map[string]any{"summary": func() {}}, want: "import_memories: invalid input"},
		{name: "reflect_memories", in: map[string]any{"limit": func() {}}, want: "reflect_memories: invalid input"},
		{name: "confirm_memory", in: map[string]any{"claim_id": func() {}}, want: "confirm_memory: invalid input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "profile-memory", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
			}
		})
	}

	confirm, _ := reg.Get("confirm_memory")
	if _, err := confirm.Invoke(context.Background(), "profile-memory", map[string]any{"decision": "keep_existing"}); err == nil || !strings.Contains(err.Error(), "claim_id is required") {
		t.Fatalf("confirm missing claim_id err = %v", err)
	}
	if _, err := confirm.Invoke(context.Background(), "profile-memory", map[string]any{"claim_id": "claim-1"}); err == nil || !strings.Contains(err.Error(), "decision is required") {
		t.Fatalf("confirm missing decision err = %v", err)
	}
}

func TestBuildDefaultMemoryTools_GranularEntryValidation(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Memory: &stubMemory{}})

	cases := []struct {
		toolName string
		field    string
	}{
		{toolName: "import_memories", field: "summary"},
	}

	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			tool, ok := reg.Get(tc.toolName)
			if !ok {
				t.Fatalf("%s not registered", tc.toolName)
			}
			properties := tool.InputSchema["properties"].(map[string]any)
			fieldSchema := properties[tc.field].(map[string]any)
			if got, want := fieldSchema["maxLength"], memoryEntryMaxLength; got != want {
				t.Fatalf("%s.%s maxLength = %v; want %d", tc.toolName, tc.field, got, want)
			}

			err := ValidateInput(tool, map[string]any{
				tc.field: strings.Repeat("x", memoryEntryMaxLength+1),
			})
			if err == nil || !strings.Contains(err.Error(), "Split large scenarios") {
				t.Fatalf("ValidateInput long %s error = %v; want split guidance", tc.field, err)
			}
		})
	}

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
	if err == nil || !strings.Contains(err.Error(), "Split large scenarios") {
		t.Fatalf("ValidateInput long remember evidence error = %v; want split guidance", err)
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

	t.Run("memory reflection error propagates", func(t *testing.T) {
		reg, _ := BuildDefault(Dependencies{Recall: stubRecall{}, Memory: stubMemoryReflectError{}})
		tool, _ := reg.Get("recall_memory")

		_, err := tool.Invoke(context.Background(), "profile-memory", map[string]any{"query": "hello"})

		if err == nil || !strings.Contains(err.Error(), "reflect failed") {
			t.Fatalf("err = %v; want reflect failed", err)
		}
	})
}
