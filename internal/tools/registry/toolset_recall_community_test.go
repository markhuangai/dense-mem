package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestBuildDefault_RecallMemoryRejectsRemovedCommunityOption(t *testing.T) {
	reg, err := BuildDefault(Dependencies{Recall: &capturingRecall{}})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("recall_memory")
	if !ok {
		t.Fatal("Get(recall_memory): not found")
	}

	properties, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("input schema properties has type %T", tool.InputSchema["properties"])
	}
	if _, ok := properties["use_communities"]; ok {
		t.Fatal("recall_memory schema retained removed use_communities")
	}

	err = ValidateInput(tool, map[string]any{"query": "hello", "use_communities": true})
	if err == nil || !strings.Contains(err.Error(), "use_communities") {
		t.Fatalf("ValidateInput error = %v", err)
	}
}

type capturingRecall struct {
	lastReq recallservice.RecallRequest
}

func (s *capturingRecall) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	s.lastReq = req
	return []recallservice.RecallHit{}, nil
}
