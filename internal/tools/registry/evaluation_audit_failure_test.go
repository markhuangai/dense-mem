//go:build evaluation

package registry

import (
	"context"
	"errors"
	"testing"

	appservice "github.com/markhuangai/dense-mem/internal/service"
)

type failingEvaluationAudit struct {
	err error
}

func (s failingEvaluationAudit) Append(context.Context, appservice.AuditLogEntry) error {
	return s.err
}

func TestEvaluationAuditFailureBlocksEvaluationTool(t *testing.T) {
	sentinel := errors.New("audit unavailable")
	reg, err := BuildActive(Dependencies{
		Dreams:          &stubDreamService{},
		EvaluationAudit: failingEvaluationAudit{err: sentinel},
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get("eval_run_dream_cycle")
	if !ok {
		t.Fatal("evaluation build omitted eval_run_dream_cycle")
	}
	_, err = tool.Invoke(context.Background(), "team-id", map[string]any{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("evaluation audit error = %v; want %v", err, sentinel)
	}
}
