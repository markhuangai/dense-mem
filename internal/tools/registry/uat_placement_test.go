package registry

import (
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestBuildActiveWiresExecutableGetMemoryPlacement(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{Remember: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	placement, ok := reg.Get(ToolGetMemoryPlacement)
	if !ok {
		t.Fatal("BuildActive did not register get_memory_placement")
	}
	if placement.Invoke == nil {
		t.Fatal("BuildActive get_memory_placement invoker is nil")
	}
	out, err := placement.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"ingest_id": "ingest-canonical",
	})
	if err != nil {
		t.Fatalf("get_memory_placement.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: placement.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["ingest_id"] != "ingest-canonical" || out["processing_state"] != string(domain.PlacementRunCompleted) {
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
	if stub.placementReq.IngestID != "ingest-canonical" {
		t.Fatalf("stub placement request not populated: %#v", stub.placementReq)
	}
}

func TestBuildActiveGetMemoryPlacementRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Remember: &stubRememberService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	placement, ok := reg.Get(ToolGetMemoryPlacement)
	if !ok {
		t.Fatal("BuildActive did not register get_memory_placement")
	}
	_, err = placement.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id":   "attacker-team",
		"ingest_id": "ingest-canonical",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("get_memory_placement.Invoke err = %v, want tenant override rejection", err)
	}
}
