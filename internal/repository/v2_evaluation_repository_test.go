package repository

import (
	"strings"
	"testing"
)

func TestV2EvaluationQueryUsesBoundFiltersForValueStatus(t *testing.T) {
	query, args, err := v2EvaluationQuery(V2EvaluationListInput{
		TeamID: "00000000-0000-0000-0000-000000000101",
		Type:   "value",
		Status: "active",
	}, 11, 7)
	if err != nil {
		t.Fatalf("v2EvaluationQuery: %v", err)
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

func TestV2EvaluationInputNormalization(t *testing.T) {
	input := normalizeV2EvaluationListInput(V2EvaluationListInput{
		TeamID: " 00000000-0000-0000-0000-000000000101 ",
		Type:   " Dream ",
		Limit:  1000,
		Status: " Active ",
		Cursor: " 10 ",
	})
	if input.Type != "hypothesis" || input.Status != "active" || input.Cursor != "10" || input.Limit != 500 {
		t.Fatalf("normalized input = %+v", input)
	}
	if offset := v2EvaluationCursorOffset("-1"); offset != 0 {
		t.Fatalf("negative cursor offset = %d; want 0", offset)
	}
}
