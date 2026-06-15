package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestRecallMemoryIgnoresDreamRecallFailure(t *testing.T) {
	dreams := &stubDreamService{recallErr: errors.New("dream recall failed")}
	reg, _ := BuildDefault(Dependencies{
		Recall: stubRecallWithHit{},
		Dreams: dreams,
	})
	tool, _ := reg.Get("recall_memory")

	out, err := tool.Invoke(context.Background(), "profile-search", map[string]any{"query": "hello"})

	if err != nil {
		t.Fatalf("recall_memory Invoke: %v", err)
	}
	results := out["results"].([]map[string]any)
	if len(results) != 1 || results[0]["tier"] != recallservice.TierFragment {
		t.Fatalf("recall_memory results = %v, want base recall hit", results)
	}
	if dreams.recallQuery != "hello" {
		t.Fatalf("dream recall query = %q, want hello", dreams.recallQuery)
	}
	related := out["related_dreams"].([]any)
	if len(related) != 0 {
		t.Fatalf("related_dreams = %v, want empty on dream recall error", related)
	}
}
