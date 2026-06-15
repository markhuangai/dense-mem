package registry

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestBuildDefault_RecallInvokerForwardsCommunityOption(t *testing.T) {
	rec := &capturingRecall{}
	reg, _ := BuildDefault(Dependencies{Recall: rec})
	tool, _ := reg.Get("recall_memory")

	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("input schema properties has type %T", tool.InputSchema["properties"])
	}
	if _, ok := properties["use_communities"]; !ok {
		t.Fatal("recall_memory schema missing use_communities")
	}

	if _, err := tool.Invoke(context.Background(), "pA", map[string]any{"query": "hello", "use_communities": true}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !rec.lastReq.UseCommunities {
		t.Fatal("UseCommunities = false; want true")
	}
}

type capturingRecall struct {
	lastReq recallservice.RecallRequest
}

func (s *capturingRecall) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	s.lastReq = req
	return []recallservice.RecallHit{}, nil
}
