package registry

import (
	"context"
	"errors"
	"testing"

	appservice "github.com/markhuangai/dense-mem/internal/service"
)

type evaluationAuditStub struct {
	entries []appservice.AuditLogEntry
}

func (s *evaluationAuditStub) Append(_ context.Context, entry appservice.AuditLogEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}

func TestEvalScoreRetrievalCaseScoresAndAudits(t *testing.T) {
	audit := &evaluationAuditStub{}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:  audit,
		EvaluationConfig: stubEvaluationConfig{enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("eval_score_retrieval_case")
	if !ok {
		t.Fatal("eval_score_retrieval_case not registered")
	}
	input := map[string]any{
		"k": 3,
		"ranked_refs": []any{
			map[string]any{"type": "fragment", "id": "irrelevant-1"},
			map[string]any{"type": "claim", "id": "claim-1"},
			map[string]any{"type": "fact", "id": "fact-1"},
		},
		"required_refs": []any{
			map[string]any{"type": "claim", "id": "claim-1", "grade": 2},
			map[string]any{"type": "fact", "id": "fact-1", "grade": 1},
		},
		"bad_refs": []any{
			map[string]any{"type": "fragment", "id": "irrelevant-1"},
		},
	}
	if err := ValidateInput(tool, input); err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}

	out, err := tool.Invoke(context.Background(), "profile-eval", input)
	if err != nil {
		t.Fatalf("eval_score_retrieval_case Invoke: %v", err)
	}
	if out["k"] != float64(3) || out["relevant_at_k"] != float64(2) || out["bad_at_k"] != float64(1) {
		t.Fatalf("score output = %v", out)
	}
	if out["recall_at_k"] != float64(1) || out["mrr"] != float64(0.5) {
		t.Fatalf("ranking metrics = %v", out)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit entries = %d; want 1", len(audit.entries))
	}
	entry := audit.entries[0]
	if entry.Operation != "EVALUATION_TOOL_CALL" || entry.EntityID != "eval_score_retrieval_case" {
		t.Fatalf("audit entry = %+v", entry)
	}
	if entry.Metadata["content_returned"] != false || entry.Metadata["page_size"] != 3 {
		t.Fatalf("audit metadata = %+v", entry.Metadata)
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
