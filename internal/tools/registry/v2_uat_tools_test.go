package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildV2UATWiresExecutableRemember(t *testing.T) {
	stub := &stubV2RememberService{}
	reg, err := BuildV2UAT(Dependencies{V2Remember: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	remember, ok := reg.Get(V2ToolRemember)
	if !ok {
		t.Fatal("BuildV2UAT did not register remember")
	}
	if remember.Invoke == nil {
		t.Fatal("BuildV2UAT remember invoker is nil")
	}
	out, err := remember.Invoke(context.Background(), "ignored-profile", map[string]any{
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err != nil {
		t.Fatalf("remember.Invoke: %v", err)
	}
	if out["ingest_id"] != "ingest-v2" {
		t.Fatalf("ingest_id = %#v, want ingest-v2", out["ingest_id"])
	}
	if stub.req.ContractVersion != domain.V2ContractVersion || len(stub.req.Evidence) != 1 {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.Evidence[0].Content != "remember this exact evidence" {
		t.Fatalf("evidence content = %q", stub.req.Evidence[0].Content)
	}
}

func TestBuildV2UATRememberRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Remember: &stubV2RememberService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	remember, ok := reg.Get(V2ToolRemember)
	if !ok {
		t.Fatal("BuildV2UAT did not register remember")
	}
	_, err = remember.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("remember.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATWiresExecutableRecallMemory(t *testing.T) {
	stub := &stubV2RecallService{}
	reg, err := BuildV2UAT(Dependencies{V2Recall: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	recall, ok := reg.Get(V2ToolRecallMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register recall_memory")
	}
	if recall.Invoke == nil {
		t.Fatal("BuildV2UAT recall_memory invoker is nil")
	}
	out, err := recall.Invoke(context.Background(), "ignored-profile", map[string]any{
		"query": "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("recall_memory.Invoke: %v", err)
	}
	if out["recall_id"] != "rec-v2" {
		t.Fatalf("recall_id = %#v, want rec-v2", out["recall_id"])
	}
	results, ok := out["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v, want one result", out["results"])
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", results[0])
	}
	if _, ok := result["score"]; ok {
		t.Fatalf("recall_memory exposed score in public output: %#v", result)
	}
	if stub.req.Query != "PostgreSQL memory" {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.ContractVersion != domain.V2ContractVersion {
		t.Fatalf("contract version = %q", stub.req.ContractVersion)
	}
}

func TestBuildV2UATRecallRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{V2Recall: &stubV2RecallService{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	recall, ok := reg.Get(V2ToolRecallMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register recall_memory")
	}
	_, err = recall.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"query":   "PostgreSQL memory",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("recall_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATExecutableToolsRequireDependencies(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: V2ToolRemember,
			args: map[string]any{
				"evidence": []any{map[string]any{"content": "remember"}},
			},
		},
		{
			name: V2ToolRecallMemory,
			args: map[string]any{
				"query": "PostgreSQL",
			},
		},
		{
			name: V2ToolResolveMemoryPlacement,
			args: map[string]any{
				"action":          string(domain.V2ResolveForget),
				"relationship_id": "relationship-v2",
				"reason":          "forget this relationship",
				"idempotency_key": "forget-1",
				"evidence":        []any{map[string]any{"content": "forget"}},
			},
		},
		{
			name: V2ToolCorrectEntityResolution,
			args: map[string]any{
				"operation":             string(domain.V2EntityCorrectionSplit),
				"source_entity_id":      "entity-source",
				"target_entity_id":      nil,
				"owned_observation_ids": []any{"obs-1"},
				"dry_run":               true,
			},
		},
		{
			name: V2ToolTraceMemory,
			args: map[string]any{
				"relationship_id": "relationship-v2",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := reg.Get(tc.name)
			if !ok || tool.Invoke == nil {
				t.Fatalf("tool %s not executable", tc.name)
			}
			_, err := tool.Invoke(context.Background(), "ignored-profile", tc.args)
			if !errors.Is(err, ErrToolUnavailable) {
				t.Fatalf("%s err = %v, want ErrToolUnavailable", tc.name, err)
			}
		})
	}
}

func TestBuildV2UATWiresExecutableTraceMemory(t *testing.T) {
	stub := &stubV2TraceContext{}
	reg, err := BuildV2UAT(Dependencies{Context: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	trace, ok := reg.Get(V2ToolTraceMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register trace_memory")
	}
	if trace.Invoke == nil {
		t.Fatal("BuildV2UAT trace_memory invoker is nil")
	}
	out, err := trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"relationship_id":          "relationship-v2",
		"include_evidence_content": false,
		"include_verification":     true,
		"include_transitions":      false,
		"max_depth":                2,
		"max_edges":                12,
		"predicate_keys":           []any{"works_on"},
		"topic":                    "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("trace_memory.Invoke: %v", err)
	}
	relationship, ok := out["relationship"].(map[string]any)
	if !ok || relationship["relationship_id"] != "relationship-v2" {
		t.Fatalf("relationship = %#v", out["relationship"])
	}
	if _, ok := out["v2_semantic"]; ok {
		t.Fatalf("trace_memory should unwrap V2 payload, got %#v", out)
	}
	if stub.req.RelationshipID != "relationship-v2" || stub.req.MaxDepth != 2 || stub.req.MaxEdges != 12 {
		t.Fatalf("trace request = %#v", stub.req)
	}
	if got := strings.Join(stub.req.PredicateKeys, ","); got != "works_on" {
		t.Fatalf("predicate_keys = %q", got)
	}
}

func TestBuildV2UATTraceRejectsTenantOverride(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{Context: &stubV2TraceContext{}})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	trace, ok := reg.Get(V2ToolTraceMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register trace_memory")
	}
	_, err = trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"team_id":         "attacker-team",
		"relationship_id": "relationship-v2",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("trace_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

type stubV2RememberService struct {
	req          memoryservice.V2RememberRequest
	placementReq memoryservice.V2GetMemoryPlacementRequest
}

func (s *stubV2RememberService) RememberV2(_ context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.req = req
	return &memoryservice.V2RememberResult{
		IngestID:          "ingest-v2",
		Status:            string(domain.V2PlacementRunQueued),
		CheckAfterSeconds: 60,
		StatusTool:        V2ToolGetMemoryPlacement,
		Items: []memoryservice.V2RememberItemResult{
			{
				ItemID:        "item-v2",
				Version:       1,
				EvidenceIndex: 0,
				Category:      string(domain.V2EvidenceProcessed),
				SearchState:   string(domain.V2SearchProjectionNotRequired),
			},
		},
		SearchState: string(domain.V2SearchProjectionNotRequired),
	}, nil
}

func (s *stubV2RememberService) GetMemoryPlacementV2(_ context.Context, req memoryservice.V2GetMemoryPlacementRequest) (*memoryservice.V2RememberResult, error) {
	s.placementReq = req
	return &memoryservice.V2RememberResult{
		IngestID:          req.IngestID,
		Status:            string(domain.V2PlacementRunCompleted),
		CheckAfterSeconds: 0,
		StatusTool:        V2ToolGetMemoryPlacement,
		Items: []memoryservice.V2RememberItemResult{{
			ItemID:        "item-v2",
			Version:       3,
			EvidenceIndex: 0,
			Category:      string(domain.V2EvidenceProcessed),
			SearchState:   string(domain.V2SearchProjectionNotRequired),
		}},
		SearchState: string(domain.V2SearchProjectionNotRequired),
	}, nil
}

type stubV2RecallService struct {
	req memoryservice.V2RecallRequest
}

func (s *stubV2RecallService) RecallV2(_ context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
	s.req = req
	return &memoryservice.V2RecallResult{
		RecallID: "rec-v2",
		Results: []memoryservice.V2RecallResultItem{{
			EvidenceID:      "evidence-v2",
			RelationshipIDs: []string{"relationship-v2"},
			Rank:            1,
			Context:         "Dense-Mem uses PostgreSQL.",
		}},
		SearchState: string(domain.V2SearchProjectionCurrent),
	}, nil
}

type stubV2TraceContext struct {
	req contextservice.TraceRequest
}

func (s *stubV2TraceContext) Trace(_ context.Context, _ string, req contextservice.TraceRequest) (*contextservice.TraceResult, error) {
	s.req = req
	return &contextservice.TraceResult{
		V2Semantic: &contextservice.V2SemanticTrace{
			Relationship: &repository.V2RelationshipTraceRecord{
				RelationshipID: "relationship-v2",
				PredicateKey:   "works_on",
			},
			EvidenceSupports: []repository.V2RelationshipEvidenceSupportRecord{{
				SupportID: "support-v2",
			}},
			StoppedReason: "max_edges",
			Truncated:     true,
		},
	}, nil
}

func (s *stubV2TraceContext) Assemble(context.Context, string, contextservice.AssembleRequest) (*contextservice.AssembleResult, error) {
	return nil, errors.New("not implemented")
}
