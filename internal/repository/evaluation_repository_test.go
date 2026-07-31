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
