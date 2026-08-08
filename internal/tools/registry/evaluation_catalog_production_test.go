//go:build !evaluation

package registry

import "testing"

func TestProductionCatalogExcludesEvaluationTools(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	for _, name := range []string{
		"eval_list_knowledge_refs",
		"eval_run_dream_cycle",
		"eval_run_recall_case",
		"eval_get_manifest",
		"eval_get_knowledge_item",
		"eval_list_recall_feedback_events",
		"eval_get_recall_feedback_event",
		"eval_score_retrieval_case",
	} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("production catalog registered %s", name)
		}
	}
}
