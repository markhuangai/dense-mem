//go:build evaluation

package registry

import (
	"reflect"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEvaluationCatalogAddsOnlyHarnessTools(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	var got []string
	for _, tool := range reg.List() {
		if IsEvaluationTool(tool.Name) {
			got = append(got, tool.Name)
		}
	}
	want := []string{"eval_list_knowledge_refs", "eval_run_dream_cycle", "eval_run_recall_case"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evaluation tools = %v; want %v", got, want)
	}
	for _, removed := range []string{
		"eval_get_manifest",
		"eval_get_knowledge_item",
		"eval_list_recall_feedback_events",
		"eval_get_recall_feedback_event",
		"eval_score_retrieval_case",
	} {
		if _, ok := reg.Get(removed); ok {
			t.Fatalf("evaluation catalog retained removed tool %s", removed)
		}
	}
}

func TestEvaluationRecallRequestInjectsServiceContract(t *testing.T) {
	req, err := evalRecallRequest(map[string]any{"query": "contract boundary"})
	if err != nil {
		t.Fatalf("evalRecallRequest: %v", err)
	}
	if req.ContractVersion != domain.ContractVersion {
		t.Fatalf("contract version = %q; want %q", req.ContractVersion, domain.ContractVersion)
	}
}
