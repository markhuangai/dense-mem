//go:build evaluation

package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEvidenceOnlyCatalogWiresEvaluationRecallCaseTo(t *testing.T) {
	recall := &stubRecallService{}
	reg, err := BuildActive(Dependencies{
		RecallBindings:     RecallBindings{Service: recall},
		EvaluationBindings: EvaluationBindings{Audit: &evaluationAuditStub{}},
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get("eval_run_recall_case")
	if !ok || tool.Invoke == nil {
		t.Fatal("BuildActive did not register executable eval_run_recall_case")
	}
	input := map[string]any{"case_id": "case-canonical", "query": "PostgreSQL memory", "limit": float64(3)}
	if err := ValidateInput(tool, input); err != nil {
		t.Fatalf("evaluation recall schema: %v", err)
	}
	out, err := tool.Invoke(contractInvokeContext("read", "write"), "ignored-profile", input)
	if err != nil {
		t.Fatalf("eval_run_recall_case.Invoke: %v", err)
	}
	if recall.req.Query != "PostgreSQL memory" || recall.req.Limit != 3 {
		t.Fatalf("recall request = %#v", recall.req)
	}
	ranked, ok := out["ranked_refs"].([]map[string]any)
	if !ok || len(ranked) != 1 || ranked[0]["type"] != "evidence" || ranked[0]["id"] != "evidence-canonical" {
		t.Fatalf("ranked refs = %#v", out["ranked_refs"])
	}
	if out["search_state"] != string(domain.SearchProjectionCurrent) {
		t.Fatalf("search_state = %#v", out["search_state"])
	}
}
