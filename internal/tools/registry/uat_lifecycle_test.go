package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/lifecycle"
)

func TestBuildActiveWiresExecutableRetractEvidence(t *testing.T) {
	stub := &stubLifecycleService{}
	reg, err := BuildActive(Dependencies{LifecycleBindings: LifecycleBindings{Service: stub}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	retract, ok := reg.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	out, err := retract.Invoke(contractInvokeContext("read", "write"), "ignored-profile", map[string]any{
		"evidence_ids":    []any{"evidence-canonical"},
		"reason":          "entered in error",
		"idempotency_key": "retract-1",
	})
	if err != nil {
		t.Fatalf("retract_evidence.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: retract.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["decision_id"] != "evidence-decision-canonical" || out["processing_state"] != "completed" {
		t.Fatalf("retract output = %#v", out)
	}
	if got := stub.retractReq.EvidenceIDs; len(got) != 1 || got[0] != "evidence-canonical" {
		t.Fatalf("stub retraction request not populated: %#v", stub.retractReq)
	}
}

func TestBuildActiveRetractEvidenceReportsDependencyValidationAndServiceErrors(t *testing.T) {
	input := map[string]any{
		"evidence_ids":    []any{"evidence-canonical"},
		"reason":          "entered in error",
		"idempotency_key": "retract-errors-1",
	}

	unavailableRegistry, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive unavailable: %v", err)
	}
	unavailable, ok := unavailableRegistry.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	_, err = unavailable.Invoke(contractInvokeContext("read", "write"), "ignored-profile", input)
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("unavailable retract error = %v", err)
	}

	registry, err := BuildActive(Dependencies{LifecycleBindings: LifecycleBindings{Service: &stubLifecycleService{}}})
	if err != nil {
		t.Fatalf("BuildActive lifecycle: %v", err)
	}
	retract, ok := registry.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	_, err = retract.Invoke(contractInvokeContext("read", "write"), "ignored-profile", map[string]any{
		"evidence_ids":    []any{"evidence-canonical"},
		"idempotency_key": "retract-errors-1",
	})
	if err == nil || !strings.Contains(err.Error(), "retract_evidence: invalid input") {
		t.Fatalf("invalid retract error = %v", err)
	}

	serviceErr := errors.New("lifecycle unavailable")
	retryRegistry, err := BuildActive(Dependencies{LifecycleBindings: LifecycleBindings{Service: &stubLifecycleService{retractErr: serviceErr}}})
	if err != nil {
		t.Fatalf("BuildActive failing lifecycle: %v", err)
	}
	retry, ok := retryRegistry.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	_, err = retry.Invoke(contractInvokeContext("read", "write"), "ignored-profile", input)
	if !errors.Is(err, serviceErr) {
		t.Fatalf("service retract error = %v", err)
	}
}

type stubLifecycleService struct {
	retractReq lifecycle.RetractEvidenceRequest
	retractErr error
}

func (s *stubLifecycleService) RetractEvidence(
	_ context.Context,
	req lifecycle.RetractEvidenceRequest,
) (*lifecycle.RetractEvidenceResult, error) {
	s.retractReq = req
	if s.retractErr != nil {
		return nil, s.retractErr
	}
	return &lifecycle.RetractEvidenceResult{
		DecisionID:           "evidence-decision-canonical",
		ProcessingState:      "completed",
		RetractedEvidenceIDs: append([]string(nil), req.EvidenceIDs...),
	}, nil
}
