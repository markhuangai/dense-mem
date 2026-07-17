package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestBuildV2UATWiresExecutableGetMemoryPlacement(t *testing.T) {
	stub := &stubV2RememberService{}
	reg, err := BuildV2UAT(Dependencies{V2Remember: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	placement, ok := reg.Get(V2ToolGetMemoryPlacement)
	if !ok {
		t.Fatal("BuildV2UAT did not register get_memory_placement")
	}
	if placement.Invoke == nil {
		t.Fatal("BuildV2UAT get_memory_placement invoker is nil")
	}
	out, err := placement.Invoke(context.Background(), "ignored-profile", map[string]any{
		"ingest_id": "ingest-v2",
	})
	if err != nil {
		t.Fatalf("get_memory_placement.Invoke: %v", err)
	}
	if out["ingest_id"] != "ingest-v2" || out["processing_state"] != string(domain.V2PlacementRunCompleted) {
		t.Fatalf("placement output = %#v", out)
	}
	items, ok := out["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("placement items = %#v", out["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["version"] != float64(3) {
		t.Fatalf("placement item = %#v", items[0])
	}
	if stub.placementReq.ContractVersion != domain.V2ContractVersion || stub.placementReq.IngestID != "ingest-v2" {
		t.Fatalf("stub placement request not populated: %#v", stub.placementReq)
	}
}

func TestBuildV2UATGetMemoryPlacementRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Remember: &stubV2RememberService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	placement, ok := reg.Get(V2ToolGetMemoryPlacement)
	if !ok {
		t.Fatal("BuildV2UAT did not register get_memory_placement")
	}
	_, err = placement.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id":   "attacker-team",
		"ingest_id": "ingest-v2",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("get_memory_placement.Invoke err = %v, want tenant override rejection", err)
	}
}
