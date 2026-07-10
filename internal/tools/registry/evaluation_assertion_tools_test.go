package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEvalListAssertionsUsesOffsetCursorAndLookahead(t *testing.T) {
	graph := &evalGraphQuery{rows: []map[string]any{
		{"assertion_id": "assertion-3"},
		{"assertion_id": "assertion-4"},
		{"assertion_id": "assertion-5"},
	}}

	page, err := evalListAssertions(context.Background(), Dependencies{GraphQuery: graph}, "profile-eval", map[string]any{"cursor": "2"}, 2, false)
	if err != nil {
		t.Fatalf("evalListAssertions: %v", err)
	}
	items := page["items"].([]map[string]any)
	if len(items) != 2 || items[0]["assertion_id"] != "assertion-3" || page["has_more"] != true || page["next_cursor"] != "4" {
		t.Fatalf("assertion page = %#v", page)
	}
	if graph.params["offset"] != int64(2) || graph.params["pageLimit"] != int64(3) {
		t.Fatalf("assertion pagination params = %#v", graph.params)
	}
	if !strings.Contains(graph.query, "SKIP $offset") || !strings.Contains(graph.query, "LIMIT $pageLimit") {
		t.Fatalf("assertion pagination query = %q", graph.query)
	}
}

func TestEvalScoreRetrievalCaseAcceptsSemanticAssertions(t *testing.T) {
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:  &evaluationAuditStub{},
		EvaluationConfig: stubEvaluationConfig{enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, _ := reg.Get("eval_score_retrieval_case")
	input := map[string]any{
		"ranked_refs": []any{
			map[string]any{"type": "assertion", "id": "assertion-1"},
		},
		"required_refs": []any{
			map[string]any{"type": "assertion", "id": "assertion-1", "grade": 1},
		},
	}
	if err := ValidateInput(tool, input); err != nil {
		t.Fatalf("ValidateInput assertion refs: %v", err)
	}
	out, err := tool.Invoke(context.Background(), "profile-eval", input)
	if err != nil {
		t.Fatalf("eval_score_retrieval_case assertion Invoke: %v", err)
	}
	if out["relevant_at_k"] != float64(1) || out["recall_at_k"] != float64(1) || out["mrr"] != float64(1) {
		t.Fatalf("assertion metrics = %v", out)
	}
}

func TestEvalToolsRequireAuditSink(t *testing.T) {
	reg, err := BuildDefault(Dependencies{EvaluationConfig: stubEvaluationConfig{enabled: true}})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("eval_score_retrieval_case")
	if !ok {
		t.Fatal("eval_score_retrieval_case not registered")
	}
	_, err = tool.Invoke(context.Background(), "profile-eval", map[string]any{
		"ranked_refs":   []any{},
		"required_refs": []any{},
	})
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("err = %v; want ErrToolUnavailable", err)
	}
}
