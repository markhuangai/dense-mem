package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildActiveWiresExecutableRetractEvidence(t *testing.T) {
	stub := &stubLifecycleService{}
	reg, err := BuildActive(Dependencies{Lifecycle: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	retract, ok := reg.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	out, err := retract.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
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
	_, err = unavailable.Invoke(contractInvokeContext("write"), "ignored-profile", input)
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("unavailable retract error = %v", err)
	}

	registry, err := BuildActive(Dependencies{Lifecycle: &stubLifecycleService{}})
	if err != nil {
		t.Fatalf("BuildActive lifecycle: %v", err)
	}
	retract, ok := registry.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	_, err = retract.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"evidence_ids":    []any{"evidence-canonical"},
		"idempotency_key": "retract-errors-1",
	})
	if err == nil || !strings.Contains(err.Error(), "retract_evidence: invalid input") {
		t.Fatalf("invalid retract error = %v", err)
	}

	serviceErr := errors.New("lifecycle unavailable")
	retryRegistry, err := BuildActive(Dependencies{Lifecycle: &stubLifecycleService{retractErr: serviceErr}})
	if err != nil {
		t.Fatalf("BuildActive failing lifecycle: %v", err)
	}
	retry, ok := retryRegistry.Get(ToolRetractEvidence)
	if !ok {
		t.Fatal("BuildActive did not register retract_evidence")
	}
	_, err = retry.Invoke(contractInvokeContext("write"), "ignored-profile", input)
	if !errors.Is(err, serviceErr) {
		t.Fatalf("service retract error = %v", err)
	}
}

func TestBuildActiveWiresExecutableCorrectRelationship(t *testing.T) {
	stub := &stubLifecycleService{}
	reg, err := BuildActive(Dependencies{Lifecycle: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	correct, ok := reg.Get(ToolCorrectRelationship)
	if !ok {
		t.Fatal("BuildActive did not register correct_relationship")
	}
	if correct.Invoke == nil {
		t.Fatal("BuildActive correct_relationship invoker is nil")
	}
	out, err := correct.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"action":           "submit",
		"relationship_id":  "relationship-source",
		"expected_version": 2,
		"patch":            map[string]any{"predicate": map[string]any{"key": "works_with"}},
		"supports":         []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 8}},
		"reason":           "predicate resolved incorrectly",
		"idempotency_key":  "relationship-correction-1",
	})
	if err != nil {
		t.Fatalf("correct_relationship.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: correct.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["submission_kind"] != "relationship_correction" || out["processing_state"] != "completed" {
		t.Fatalf("correct output = %#v", out)
	}
	if stub.correctReq.Action != "submit" || stub.correctReq.RelationshipID != "relationship-source" {
		t.Fatalf("stub correct request not populated: %#v", stub.correctReq)
	}
}

func TestBuildActiveCorrectRelationshipRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Lifecycle: &stubLifecycleService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	correct, ok := reg.Get(ToolCorrectRelationship)
	if !ok {
		t.Fatal("BuildActive did not register correct_relationship")
	}
	_, err = correct.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"team_id":          "attacker-team",
		"action":           "submit",
		"relationship_id":  "relationship-source",
		"expected_version": 2,
		"patch":            map[string]any{"predicate": map[string]any{"key": "works_with"}},
		"supports":         []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 8}},
		"reason":           "predicate resolved incorrectly",
		"idempotency_key":  "relationship-correction-1",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("correct_relationship.Invoke err = %v, want tenant override rejection", err)
	}
}

type stubLifecycleService struct {
	correctReq memoryservice.CorrectRelationshipRequest
	retractReq memoryservice.RetractEvidenceRequest
	retractErr error
}

func (s *stubLifecycleService) CorrectRelationship(
	_ context.Context,
	req memoryservice.CorrectRelationshipRequest,
) (*memoryservice.CorrectRelationshipReceipt, error) {
	s.correctReq = req
	return &memoryservice.CorrectRelationshipReceipt{
		ContractVersion: domain.ContractVersion,
		SubmissionID:    "correction-canonical", SubmissionKind: "relationship_correction",
		ProcessingState: "completed", CorrelationID: "correlation-canonical",
	}, nil
}

func (s *stubLifecycleService) RetractEvidence(
	_ context.Context,
	req memoryservice.RetractEvidenceRequest,
) (*memoryservice.RetractEvidenceResult, error) {
	s.retractReq = req
	if s.retractErr != nil {
		return nil, s.retractErr
	}
	return &memoryservice.RetractEvidenceResult{
		DecisionID:                      "evidence-decision-canonical",
		ProcessingState:                 "completed",
		RetractedEvidenceIDs:            append([]string(nil), req.EvidenceIDs...),
		AffectedRelationshipCount:       1,
		PendingRelationshipCount:        1,
		RetainedActiveRelationshipCount: 0,
	}, nil
}
