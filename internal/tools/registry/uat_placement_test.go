package registry

import (
	"strings"
	"testing"
)

func TestBuildActiveWiresExecutableSubmissionStatus(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{Remember: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	status, ok := reg.Get(ToolGetSubmissionStatus)
	if !ok || status.Invoke == nil {
		t.Fatal("BuildActive did not register get_submission_status")
	}
	out, err := status.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"submission_id": "ingest-canonical",
	})
	if err != nil {
		t.Fatalf("get_submission_status.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: status.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["submission_id"] != "ingest-canonical" || out["processing_state"] != "completed" {
		t.Fatalf("status output = %#v", out)
	}
	if stub.statusReq.SubmissionID != "ingest-canonical" {
		t.Fatalf("stub status request not populated: %#v", stub.statusReq)
	}
}

func TestBuildActiveSubmissionStatusRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Remember: &stubRememberService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	status, ok := reg.Get(ToolGetSubmissionStatus)
	if !ok {
		t.Fatal("BuildActive did not register get_submission_status")
	}
	_, err = status.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id":       "attacker-team",
		"submission_id": "ingest-canonical",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("get_submission_status.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveDoesNotRegisterRemovedPlacementTools(t *testing.T) {
	reg, err := BuildActive(Dependencies{Remember: &stubRememberService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	for _, name := range []string{"get_memory_placement", "resolve_memory_placement"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("removed tool %s remains registered", name)
		}
	}
}
