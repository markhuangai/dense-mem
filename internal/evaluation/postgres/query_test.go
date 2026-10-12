package postgres

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
		Type:   " relationship ",
		Limit:  1000,
		Status: " Active ",
		Cursor: " 10 ",
	})
	if input.Type != "relationship" || input.Status != "active" || input.Cursor != "10" || input.Limit != 500 {
		t.Fatalf("normalized input = %+v", input)
	}
	if offset := evaluationCursorOffset("-1"); offset != 0 {
		t.Fatalf("negative cursor offset = %d; want 0", offset)
	}
}

func TestEvaluationQueryValidationBranches(t *testing.T) {
	validTeam := "00000000-0000-0000-0000-000000000101"
	if err := validateEvaluationListInput(EvaluationListInput{TeamID: "bad", Type: "value"}); err == nil {
		t.Fatal("invalid team was accepted")
	}
	if err := validateEvaluationListInput(EvaluationListInput{TeamID: validTeam, Type: "unknown"}); err == nil {
		t.Fatal("invalid type was accepted")
	}
	if err := validateEvaluationGetInput(EvaluationGetInput{TeamID: validTeam, Type: "value", ID: "bad"}); err == nil {
		t.Fatal("invalid id was accepted")
	}
	if err := validateEvaluationGetInput(EvaluationGetInput{TeamID: validTeam, Type: "unknown", ID: validTeam}); err == nil {
		t.Fatal("invalid get type was accepted")
	}
	if got := normalizeEvaluationType(" FRAGMENT "); got != "evidence" {
		t.Fatalf("fragment normalization = %q", got)
	}
	if got := normalizeEvaluationType(" VALUE "); got != "value" {
		t.Fatalf("canonical type normalization = %q", got)
	}
	if got := normalizeEvaluationType("custom"); got != "custom" {
		t.Fatalf("custom normalization = %q", got)
	}
	if got := evaluationCursorOffset("bad"); got != 0 {
		t.Fatalf("invalid cursor offset = %d", got)
	}
	if _, _, err := evaluationQuery(EvaluationListInput{TeamID: validTeam, Type: "hypothesis"}, 1, 0); err == nil {
		t.Fatal("retired hypothesis type was accepted")
	}
}
