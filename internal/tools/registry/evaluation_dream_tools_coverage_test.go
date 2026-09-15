//go:build evaluation

package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/dream"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

type evaluationDreamApplicationStub struct {
	req dream.RunCycleRequest
	err error
}

func (s *evaluationDreamApplicationStub) ListKnowledgeRefs(context.Context, string, string, int, string, string, bool) (map[string]any, error) {
	return nil, nil
}
func (s *evaluationDreamApplicationStub) RunRecallCase(context.Context, string, recallcontract.Request, string, string, bool, bool) (map[string]any, error) {
	return nil, nil
}
func (s *evaluationDreamApplicationStub) RunDreamCycle(_ context.Context, _ string, req dream.RunCycleRequest) (map[string]any, error) {
	s.req = req
	return map[string]any{"ok": true}, s.err
}

func TestEvaluationDreamToolSchemaAndInputConversion(t *testing.T) {
	if tool := evalRunDreamCycleTool(Dependencies{}); tool.Invoke == nil || len(tool.InputSchema) == 0 {
		t.Fatal("evaluation dream tool was not constructed")
	}
	seed := []any{
		map[string]any{"hypothesis": " claim ", "what_if": "what", "possible_outcome": "outcome", "rationale": "because", "likelihood": float64(0.5), "confidence": float64(0.7), "source_refs": []any{map[string]any{"type": "relationship", "id": "rel-1"}, "bad"}},
		"bad item",
	}
	app := &evaluationDreamApplicationStub{}
	tool := evalRunDreamCycleTool(Dependencies{EvaluationBindings: EvaluationBindings{Application: app}})
	result, err := tool.Invoke(context.Background(), "team-1", map[string]any{"max_outputs": float64(3), "seed_dreams": seed})
	if err != nil || result["ok"] != true {
		t.Fatalf("evaluation dream invoke = %#v, %v", result, err)
	}
	if !app.req.Manual || app.req.MaxOutputs != 3 || len(app.req.SeedDreams) != 1 || app.req.SeedDreams[0].Hypothesis != "claim" || len(app.req.SeedDreams[0].SourceRefs) != 1 {
		t.Fatalf("converted dream request = %+v", app.req)
	}
	if got := seedDreamsInput(nil); got != nil || seedDreamsInput("bad") != nil || seedDreamSourceRefsInput(nil) != nil {
		t.Fatal("invalid seed input was not ignored")
	}
	app.err = errors.New("dream failed")
	if _, err := tool.Invoke(context.Background(), "team-1", nil); err == nil || !errors.Is(err, app.err) {
		t.Fatalf("provider error = %v", err)
	}
	if _, err := evalRunDreamCycleTool(Dependencies{}).Invoke(context.Background(), "team-1", nil); !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("missing evaluation application error = %v", err)
	}
}
