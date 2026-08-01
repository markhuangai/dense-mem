package registry

import (
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestBuildActiveWiresExecutableGetSubmissionStatus(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{Remember: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	placement, ok := reg.Get(ToolGetSubmissionStatus)
	if !ok {
		t.Fatal("BuildActive did not register get_submission_status")
	}
	if placement.Invoke == nil {
		t.Fatal("BuildActive get_submission_status invoker is nil")
	}
	out, err := placement.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"submission_id": "submission-canonical",
	})
	if err != nil {
		t.Fatalf("get_submission_status.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: placement.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["submission_id"] != "submission-canonical" || out["processing_state"] != string(domain.SubmissionCompleted) {
		t.Fatalf("submission status output = %#v", out)
	}
	items, ok := out["evidence"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("submission evidence = %#v", out["evidence"])
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["evidence_index"] != float64(0) {
		t.Fatalf("submission evidence item = %#v", items[0])
	}
	if stub.statusReq.SubmissionID != "submission-canonical" {
		t.Fatalf("stub status request not populated: %#v", stub.statusReq)
	}
}

func TestBuildActiveGetSubmissionStatusRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Remember: &stubRememberService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	placement, ok := reg.Get(ToolGetSubmissionStatus)
	if !ok {
		t.Fatal("BuildActive did not register get_submission_status")
	}
	_, err = placement.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id":       "attacker-team",
		"submission_id": "submission-canonical",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("get_submission_status.Invoke err = %v, want tenant override rejection", err)
	}
}
