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
		"recall_memory", "assemble_context", "trace_memory",
	}
	for _, name := range required {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
	removed := []string{
		"dispute_memory_placement", "import_memories", "reflect_memories", "confirm_memory",
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
		{"remember", structuredRememberInput("hello")},
		{"get_memory_placement", map[string]any{"ingest_id": "ingest-1"}},
		{"resolve_memory_placement", map[string]any{"ingest_id": "ingest-1", "decision": "acknowledge"}},
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
		{"remember", structuredRememberInput("hello"), "write"},
		{"get_memory_placement", map[string]any{"ingest_id": "ingest-1"}, "read"},
		{"resolve_memory_placement", map[string]any{"ingest_id": "ingest-1", "decision": "acknowledge"}, "write"},
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

func TestRememberRejectsLegacyShapeAndRequiresStructuredProposal(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{Memory: &stubMemory{}})
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

	if err := ValidateInput(tool, NormalizeInput(tool, legacy)); err == nil {
		t.Fatal("legacy remember shape passed V2 schema validation")
	}
	if err := ValidateInput(tool, map[string]any{"evidence": []any{map[string]any{"content": "missing proposal"}}}); err == nil || !strings.Contains(err.Error(), "proposal") {
		t.Fatalf("missing proposal error = %v", err)
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
		{name: "resolve_memory_placement", in: map[string]any{"ingest_id": func() {}}, want: "resolve_memory_placement: invalid input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, _ := reg.Get(tc.name)
			_, err := tool.Invoke(context.Background(), "profile-memory", tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s err = %v; want %q", tc.name, err, tc.want)
			}
		})
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
		"proposal": structuredRememberInput("x")["proposal"],
	})
	if err == nil || !strings.Contains(err.Error(), "Split large scenarios") {
		t.Fatalf("ValidateInput long remember evidence error = %v; want split guidance", err)
	}
}

func structuredRememberInput(content string) map[string]any {
	return map[string]any{
		"evidence": []any{map[string]any{"content": content}},
		"proposal": map[string]any{
			"entities": []any{
				map[string]any{"ref": "subject", "name": "Subject", "type": "concept"},
				map[string]any{"ref": "object", "name": "Object", "type": "concept"},
			},
			"relationships": []any{map[string]any{
				"proposal_id": "relationship-1", "subject_ref": "subject", "predicate": "relates_to", "object_ref": "object",
				"policy_family": "multi_state", "polarity": "+", "modality": "assertion",
				"evidence": []any{map[string]any{"evidence_index": 0, "start": 0, "end": len(content)}},
			}},
		},
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
