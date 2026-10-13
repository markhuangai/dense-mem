//go:build evaluation

package registry

import (
	"context"
	"errors"
	"testing"

	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type failingEvaluationAudit struct {
	err error
}

func (s failingEvaluationAudit) Append(context.Context, accessservice.AuditLogEntry) error {
	return s.err
}

func TestEvaluationAuditFailureBlocksEvaluationTool(t *testing.T) {
	sentinel := errors.New("audit unavailable")
	reg, err := BuildActive(Dependencies{
		EvaluationBindings: EvaluationBindings{Audit: failingEvaluationAudit{err: sentinel}},
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get("eval_list_knowledge_refs")
	if !ok {
		t.Fatal("evaluation build omitted eval_list_knowledge_refs")
	}
	_, err = tool.Invoke(context.Background(), "team-id", map[string]any{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("evaluation audit error = %v; want %v", err, sentinel)
	}
}
