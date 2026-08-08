//go:build evaluation

package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestBuildActiveWiresEvaluationRecallCaseTo(t *testing.T) {
	recall := &stubRecallService{}
	reg, err := BuildActive(Dependencies{
		Recall:          recall,
		EvaluationAudit: &evaluationAuditStub{},
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get("eval_run_recall_case")
	if !ok || tool.Invoke == nil {
		t.Fatal("BuildActive did not register executable eval_run_recall_case")
	}
	out, err := tool.Invoke(contractInvokeContext("read", "write"), "ignored-profile", map[string]any{
		"case_id":                "case-canonical",
		"query":                  "PostgreSQL memory",
		"limit":                  float64(3),
		"known_evidence_ids":     []any{"evidence-known"},
		"known_relationship_ids": []any{"relationship-known"},
		"expand_from_entity_ids": []any{"entity-expand"},
	})
	if err != nil {
		t.Fatalf("eval_run_recall_case.Invoke: %v", err)
	}
	if recall.req.Query != "PostgreSQL memory" || recall.req.Limit != 3 {
		t.Fatalf("recall request = %#v", recall.req)
	}
	if len(recall.req.KnownEvidenceIDs) != 1 || recall.req.KnownEvidenceIDs[0] != "evidence-known" {
		t.Fatalf("known evidence ids = %#v", recall.req.KnownEvidenceIDs)
	}
	if len(recall.req.KnownRelationshipIDs) != 1 || recall.req.KnownRelationshipIDs[0] != "relationship-known" {
		t.Fatalf("known relationship ids = %#v", recall.req.KnownRelationshipIDs)
	}
	if len(recall.req.ExpandFromEntityIDs) != 1 || recall.req.ExpandFromEntityIDs[0] != "entity-expand" {
		t.Fatalf("expand entity ids = %#v", recall.req.ExpandFromEntityIDs)
	}
	ranked, ok := out["ranked_refs"].([]map[string]any)
	if !ok || len(ranked) != 1 || ranked[0]["type"] != "evidence" || ranked[0]["id"] != "evidence-canonical" {
		t.Fatalf("ranked refs = %#v", out["ranked_refs"])
	}
	if out["search_state"] != string(domain.SearchProjectionCurrent) {
		t.Fatalf("search_state = %#v", out["search_state"])
	}
}
