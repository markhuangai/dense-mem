package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildV2UATWiresExecutableResolveMemoryPlacementForget(t *testing.T) {
	stub := &stubV2LifecycleService{}
	reg, err := BuildV2UAT(Dependencies{V2Lifecycle: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	resolve, ok := reg.Get(V2ToolResolveMemoryPlacement)
	if !ok {
		t.Fatal("BuildV2UAT did not register resolve_memory_placement")
	}
	if resolve.Invoke == nil {
		t.Fatal("BuildV2UAT resolve_memory_placement invoker is nil")
	}
	out, err := resolve.Invoke(context.Background(), "ignored-profile", map[string]any{
		"action":          string(domain.V2ResolveForget),
		"relationship_id": "relationship-v2",
		"reason":          "forget this relationship",
		"idempotency_key": "forget-1",
		"evidence": []any{
			map[string]any{"content": "The user asked to forget it."},
		},
	})
	if err != nil {
		t.Fatalf("resolve_memory_placement.Invoke: %v", err)
	}
	if out["decision_id"] != "decision-v2" || out["processing_state"] != string(domain.V2PlacementRunCompleted) {
		t.Fatalf("resolve output = %#v", out)
	}
	if stub.req.ContractVersion != domain.V2ContractVersion ||
		stub.req.Action != domain.V2ResolveForget ||
		stub.req.RelationshipID != "relationship-v2" ||
		stub.req.Message != "forget this relationship" {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
}

func TestBuildV2UATResolveMemoryPlacementRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Lifecycle: &stubV2LifecycleService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	resolve, ok := reg.Get(V2ToolResolveMemoryPlacement)
	if !ok {
		t.Fatal("BuildV2UAT did not register resolve_memory_placement")
	}
	_, err = resolve.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id":         "attacker-team",
		"action":          string(domain.V2ResolveForget),
		"relationship_id": "relationship-v2",
		"reason":          "forget this relationship",
		"idempotency_key": "forget-1",
		"evidence": []any{
			map[string]any{"content": "The user asked to forget it."},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("resolve_memory_placement.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATWiresExecutableCorrectEntityResolution(t *testing.T) {
	stub := &stubV2LifecycleService{}
	reg, err := BuildV2UAT(Dependencies{V2Lifecycle: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	correct, ok := reg.Get(V2ToolCorrectEntityResolution)
	if !ok {
		t.Fatal("BuildV2UAT did not register correct_entity_resolution")
	}
	if correct.Invoke == nil {
		t.Fatal("BuildV2UAT correct_entity_resolution invoker is nil")
	}
	out, err := correct.Invoke(context.Background(), "ignored-profile", map[string]any{
		"operation":             string(domain.V2EntityCorrectionSplit),
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
	if out["impact_token"] != "plan-v2" {
		t.Fatalf("correct output = %#v", out)
	}
	if stub.correctReq.ContractVersion != domain.V2ContractVersion ||
		stub.correctReq.Action != domain.V2EntityCorrectionSplit ||
		stub.correctReq.SourceEntityID != "entity-source" ||
		strings.Join(stub.correctReq.SelectedObservationIDs, ",") != "obs-1" {
		t.Fatalf("stub correct request not populated: %#v", stub.correctReq)
	}
}

func TestBuildV2UATCorrectEntityResolutionRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Lifecycle: &stubV2LifecycleService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	correct, ok := reg.Get(V2ToolCorrectEntityResolution)
	if !ok {
		t.Fatal("BuildV2UAT did not register correct_entity_resolution")
	}
	_, err = correct.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id":               "attacker-team",
		"operation":             string(domain.V2EntityCorrectionSplit),
		"source_entity_id":      "entity-source",
		"owned_observation_ids": []any{"obs-1"},
		"dry_run":               true,
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("correct_entity_resolution.Invoke err = %v, want tenant override rejection", err)
	}
}

type stubV2LifecycleService struct {
	req        memoryservice.V2ResolveMemoryPlacementRequest
	correctReq memoryservice.V2CorrectEntityResolutionRequest
}

func (s *stubV2LifecycleService) ResolveMemoryPlacementV2(
	_ context.Context,
	req memoryservice.V2ResolveMemoryPlacementRequest,
) (*memoryservice.V2ResolveMemoryPlacementResult, error) {
	s.req = req
	return &memoryservice.V2ResolveMemoryPlacementResult{
		DecisionID:    "decision-v2",
		Status:        string(domain.V2PlacementRunCompleted),
		ImpactSummary: "relationship retracted",
	}, nil
}

func (s *stubV2LifecycleService) CorrectEntityResolutionV2(
	_ context.Context,
	req memoryservice.V2CorrectEntityResolutionRequest,
) (*memoryservice.V2CorrectEntityResolutionResult, error) {
	s.correctReq = req
	return &memoryservice.V2CorrectEntityResolutionResult{
		DryRun:        req.DryRun,
		PlanToken:     "plan-v2",
		SelectedIDs:   req.SelectedObservationIDs,
		BlockedIDs:    []string{},
		ImpactSummary: "split planned",
	}, nil
}
