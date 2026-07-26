package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildActiveWiresExecutableResolveMemoryPlacementForget(t *testing.T) {
	stub := &stubLifecycleService{}
	reg, err := BuildActive(Dependencies{Lifecycle: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	resolve, ok := reg.Get(ToolResolveMemoryPlacement)
	if !ok {
		t.Fatal("BuildActive did not register resolve_memory_placement")
	}
	if resolve.Invoke == nil {
		t.Fatal("BuildActive resolve_memory_placement invoker is nil")
	}
	out, err := resolve.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"action":          string(domain.ResolveForget),
		"relationship_id": "relationship-canonical",
		"message":         "forget this relationship",
		"idempotency_key": "forget-1",
		"evidence": []any{
			map[string]any{"content": "The user asked to forget it."},
		},
	})
	if err != nil {
		t.Fatalf("resolve_memory_placement.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: resolve.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["decision_id"] != "decision-canonical" || out["processing_state"] != string(domain.PlacementRunCompleted) {
		t.Fatalf("resolve output = %#v", out)
	}
	if stub.req.Action != domain.ResolveForget || stub.req.RelationshipID != "relationship-canonical" {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
}

func TestBuildActiveResolveMemoryPlacementRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Lifecycle: &stubLifecycleService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	resolve, ok := reg.Get(ToolResolveMemoryPlacement)
	if !ok {
		t.Fatal("BuildActive did not register resolve_memory_placement")
	}
	_, err = resolve.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"team_id":         "attacker-team",
		"action":          string(domain.ResolveForget),
		"relationship_id": "relationship-canonical",
		"message":         "forget this relationship",
		"idempotency_key": "forget-1",
		"evidence": []any{
			map[string]any{"content": "The user asked to forget it."},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("resolve_memory_placement.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveWiresExecutableCorrectEntityResolution(t *testing.T) {
	stub := &stubLifecycleService{}
	reg, err := BuildActive(Dependencies{Lifecycle: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	correct, ok := reg.Get(ToolCorrectEntityResolution)
	if !ok {
		t.Fatal("BuildActive did not register correct_entity_resolution")
	}
	if correct.Invoke == nil {
		t.Fatal("BuildActive correct_entity_resolution invoker is nil")
	}
	out, err := correct.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"operation":             string(domain.EntityCorrectionSplit),
		"source_entity_id":      "entity-source",
		"target_entity_id":      nil,
		"owned_observation_ids": []any{"obs-1"},
		"dry_run":               true,
		"idempotency_key":       "split-1",
		"evidence": []any{
			map[string]any{"content": "The selected Mark mention is a different person."},
		},
	})
	if err != nil {
		t.Fatalf("correct_entity_resolution.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: correct.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["impact_token"] != "plan-canonical" {
		t.Fatalf("correct output = %#v", out)
	}
	if stub.correctReq.Operation != domain.EntityCorrectionSplit || stub.correctReq.SourceEntityID != "entity-source" {
		t.Fatalf("stub correct request not populated: %#v", stub.correctReq)
	}
}

func TestBuildActiveCorrectEntityResolutionRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Lifecycle: &stubLifecycleService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	correct, ok := reg.Get(ToolCorrectEntityResolution)
	if !ok {
		t.Fatal("BuildActive did not register correct_entity_resolution")
	}
	_, err = correct.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"team_id":               "attacker-team",
		"operation":             string(domain.EntityCorrectionSplit),
		"source_entity_id":      "entity-source",
		"target_entity_id":      nil,
		"owned_observation_ids": []any{"obs-1"},
		"dry_run":               true,
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("correct_entity_resolution.Invoke err = %v, want tenant override rejection", err)
	}
}

type stubLifecycleService struct {
	req        memoryservice.ResolveMemoryPlacementRequest
	correctReq memoryservice.CorrectEntityResolutionRequest
}

func (s *stubLifecycleService) ResolveMemoryPlacement(
	_ context.Context,
	req memoryservice.ResolveMemoryPlacementRequest,
) (*memoryservice.ResolveMemoryPlacementResult, error) {
	s.req = req
	return &memoryservice.ResolveMemoryPlacementResult{
		DecisionID:      "decision-canonical",
		ProcessingState: string(domain.PlacementRunCompleted),
		ImpactSummary:   "relationship retracted",
	}, nil
}

func (s *stubLifecycleService) CorrectEntityResolution(
	_ context.Context,
	req memoryservice.CorrectEntityResolutionRequest,
) (*memoryservice.CorrectEntityResolutionResult, error) {
	s.correctReq = req
	return &memoryservice.CorrectEntityResolutionResult{
		DryRun:                          req.DryRun,
		ImpactToken:                     "plan-canonical",
		SelectedObservationIDs:          req.OwnedObservationIDs,
		BlockedObservationIDs:           []string{},
		RelationshipChanges:             []map[string]any{},
		EntityCandidates:                []map[string]any{},
		UnchangedCrossProfileReferences: []map[string]any{},
	}, nil
}
