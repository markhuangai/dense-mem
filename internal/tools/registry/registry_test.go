package registry

import (
	"context"
	"errors"
	"testing"
)

func TestRegistry_RegisterAndList(t *testing.T) {
	r := New()
	if err := r.Register(Tool{Name: "remember", Description: "store memory"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("List length = %d; want 1", len(list))
	}
	if list[0].Name != "remember" {
		t.Errorf("List[0].Name = %q; want remember", list[0].Name)
	}
}

func TestRegistry_RejectDuplicate(t *testing.T) {
	r := New()
	if err := r.Register(Tool{Name: "remember"}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.Register(Tool{Name: "remember"})
	if err == nil {
		t.Fatal("duplicate Register expected to return an error")
	}
}

func TestRegistry_RejectEmptyName(t *testing.T) {
	r := New()
	if err := r.Register(Tool{Name: ""}); err == nil {
		t.Fatal("empty-name Register expected to return an error")
	}
}

func TestRegistry_RejectNonUnderscoreCaseNames(t *testing.T) {
	r := New()
	invalid := []string{
		"keyword-search",
		"Keyword_Search",
		"keyword.search",
		"keyword/search",
		"keyword search",
		"_keyword_search",
		"keywordSearch",
		"a1234567890123456789012345678901234567890123456789012345678901234",
	}
	for _, name := range invalid {
		if err := r.Register(Tool{Name: name}); err == nil {
			t.Errorf("Register(%q) expected underscore_case validation error", name)
		}
	}
}

func TestRegistry_Get_ReturnsFalseForMissing(t *testing.T) {
	r := New()
	_, ok := r.Get("nope")
	if ok {
		t.Error("Get on missing tool must return ok=false")
	}
}

func TestRegistry_InvokeProxiesToBoundFunc(t *testing.T) {
	r := New()
	called := false
	var gotProfile string
	if err := r.Register(Tool{
		Name: "remember",
		Invoke: func(ctx context.Context, profileID string, _ map[string]any) (map[string]any, error) {
			called = true
			gotProfile = profileID
			return map[string]any{"ok": true}, nil
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := r.Get("remember")
	if !ok {
		t.Fatal("Get returned ok=false for just-registered tool")
	}
	out, err := tool.Invoke(context.Background(), "pA", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !called {
		t.Error("Invoke did not call bound func")
	}
	if gotProfile != "pA" {
		t.Errorf("bound func received profile %q; want pA", gotProfile)
	}
	if out["ok"] != true {
		t.Errorf("Invoke output[\"ok\"] = %v; want true", out["ok"])
	}
}

func TestRegistry_InvokePropagatesError(t *testing.T) {
	r := New()
	sentinel := errors.New("boom")
	_ = r.Register(Tool{
		Name: "x",
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return nil, sentinel
		},
	})
	tool, _ := r.Get("x")
	_, err := tool.Invoke(context.Background(), "pA", nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("Invoke err = %v; want sentinel", err)
	}
}

func TestRegistry_List_SortedAlphabetically(t *testing.T) {
	r := New()
	_ = r.Register(Tool{Name: "semantic_search"})
	_ = r.Register(Tool{Name: "graph_query"})
	_ = r.Register(Tool{Name: "remember"})
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List length = %d; want 3", len(got))
	}
	want := []string{"graph_query", "remember", "semantic_search"}
	for i, n := range want {
		if got[i].Name != n {
			t.Errorf("List[%d] = %q; want %q", i, got[i].Name, n)
		}
	}
}

// TestToolRegistry_ListReturnsAllRegistered — backpressure test + AC-33.
// Confirms the registry returns every registered tool through List(), in a stable
// order for MCP consumers.
func TestToolRegistry_ListReturnsAllRegistered(t *testing.T) {
	r := New()
	names := []string{
		"list_recent_memories",
		"recall_memory",
		"remember",
		"keyword_search",
		"semantic_search",
		"graph_query",
	}
	for _, n := range names {
		if err := r.Register(Tool{Name: n}); err != nil {
			t.Fatalf("register %q: %v", n, err)
		}
	}
	got := r.List()
	if len(got) != len(names) {
		t.Fatalf("List length = %d; want %d", len(got), len(names))
	}
	// Check every registered name appears exactly once.
	seen := make(map[string]int, len(got))
	for _, tl := range got {
		seen[tl.Name]++
	}
	for _, n := range names {
		if seen[n] != 1 {
			t.Errorf("name %q appeared %d times in List(); want 1", n, seen[n])
		}
	}
}

func TestToolVisibleRuntimeGates(t *testing.T) {
	ctx := context.Background()
	if !IsEvaluationTool("eval_run_dream_cycle") || IsEvaluationTool("remember") {
		t.Fatal("IsEvaluationTool returned unexpected result")
	}
	if EvaluationEnabled(ctx, nil) || EvaluationEnabled(ctx, stubEvaluationConfig{err: errors.New("config failed")}) {
		t.Fatal("EvaluationEnabled should fail closed")
	}
	if ToolVisible(ctx, Tool{Name: "eval_get_manifest"}, nil) {
		t.Fatal("evaluation tool should be hidden without enabled config")
	}
	if !ToolVisible(ctx, Tool{Name: "eval_get_manifest"}, stubEvaluationConfig{enabled: true}) {
		t.Fatal("evaluation tool should be visible with enabled config")
	}
	if ToolVisible(ctx, Tool{Name: EvalListRecallFeedbackEventsToolName}, stubRecallFeedbackConfig{enabled: false}) {
		t.Fatal("recall feedback event tool should be hidden when feedback is disabled")
	}
	if !ToolVisible(ctx, Tool{Name: EvalListRecallFeedbackEventsToolName}, stubRecallFeedbackConfig{enabled: true}) {
		t.Fatal("recall feedback event tool should be visible when feedback is enabled")
	}
	if !ToolVisible(ctx, Tool{Name: "ordinary_tool"}, nil) {
		t.Fatal("ordinary tools should be visible by default")
	}
}
