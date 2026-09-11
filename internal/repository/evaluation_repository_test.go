package repository

import (
	"strings"
	"testing"
)

func TestEvaluationQueryUsesBoundFiltersForValueStatus(t *testing.T) {
	query, args, err := evaluationQuery(EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "value",
		Status: "active",
	}, 11, 7)
	if err != nil {
		t.Fatalf("evaluationQuery: %v", err)
	}
	if !strings.Contains(query, "AND status = ?") {
		t.Fatalf("value query did not include status filter:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("args = %#v; want team, status, limit, offset", args)
	}
	if args[1] != "active" || args[2] != 11 || args[3] != 7 {
		t.Fatalf("args = %#v", args)
	}
}

func TestEvaluationInputNormalization(t *testing.T) {
	input := normalizeEvaluationListInput(EvaluationListInput{
		TeamID: " 00000000-0000-0000-0000-000000000101 ",
		Type:   " Dream ",
		Limit:  1000,
		Status: " Active ",
		Cursor: " 10 ",
	})
	if input.Type != "hypothesis" || input.Status != "active" || input.Cursor != "10" || input.Limit != 500 {
		t.Fatalf("normalized input = %+v", input)
	}
	if offset := evaluationCursorOffset("-1"); offset != 0 {
		t.Fatalf("negative cursor offset = %d; want 0", offset)
	}
}

func TestEvaluationHypothesisQueryExcludesCanonicalAliases(t *testing.T) {
	query, _, err := evaluationQuery(EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "hypothesis",
	}, 10, 0)
	if err != nil {
		t.Fatalf("evaluationQuery: %v", err)
	}
	if !strings.Contains(query, "canonical_hypothesis_id IS NULL") {
		t.Fatalf("hypothesis query did not exclude canonical aliases:\n%s", query)
	}
}

func TestEvaluationQueryUsesInjectedHypothesisProvider(t *testing.T) {
	called := false
	query, args, err := evaluationQueryWithHypothesis(
		EvaluationListInput{TeamID: "00000000-0000-0000-0000-000000000101", Type: "hypothesis"},
		2,
		3,
		func(input EvaluationListInput, limit, offset int, ids ...string) (string, []any, error) {
			called = true
			if input.Type != "hypothesis" || limit != 2 || offset != 3 || len(ids) != 1 || ids[0] != "hypothesis-id" {
				t.Fatalf("injected query input = %+v limit=%d offset=%d ids=%v", input, limit, offset, ids)
			}
			return "SELECT injected", []any{"team-id", "hypothesis-id"}, nil
		},
		"hypothesis-id",
	)
	if err != nil {
		t.Fatalf("evaluationQueryWithHypothesis: %v", err)
	}
	if !called {
		t.Fatal("injected Hypothesis query was not called")
	}
	if query != "SELECT injected" {
		t.Fatalf("query = %q", query)
	}
	if len(args) != 2 || args[0] != "team-id" || args[1] != "hypothesis-id" {
		t.Fatalf("args = %#v", args)
	}
}
