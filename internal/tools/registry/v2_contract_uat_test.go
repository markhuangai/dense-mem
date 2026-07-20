package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildV2UATWiresExecutableRemember(t *testing.T) {
	stub := &stubV2RememberService{}
	reg, err := BuildV2UAT(Dependencies{V2Remember: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	correctionTargetID := uuid.NewString()
	remember, ok := reg.Get(V2ToolRemember)
	if !ok {
		t.Fatal("BuildV2UAT did not register remember")
	}
	if remember.Invoke == nil {
		t.Fatal("BuildV2UAT remember invoker is nil")
	}
	out, err := remember.Invoke(v2ContractInvokeContext("write"), "ignored-profile", map[string]any{
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL."},
		},
		"proposal": map[string]any{
			"entities": []any{
				map[string]any{"ref": "entity:dense-mem", "name": "Dense-Mem", "entity_kind": "project"},
				map[string]any{"ref": "entity:postgres", "name": "PostgreSQL", "entity_kind": "product"},
			},
			"relationships": []any{
				map[string]any{
					"proposal_id": "rel:uses",
					"subject_ref": "entity:dense-mem",
					"predicate":   "uses",
					"object_ref":  "entity:postgres",
					"correction_target": map[string]any{
						"relationship_id":  correctionTargetID,
						"expected_version": 2,
					},
					"evidence": []any{
						map[string]any{"evidence_index": 0, "start": 0, "end": 25},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("remember.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: remember.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["ingest_id"] != "ingest-v2" {
		t.Fatalf("ingest_id = %#v, want ingest-v2", out["ingest_id"])
	}
	if out["processing_state"] != string(domain.V2PlacementRunQueued) || out["correlation_id"] != "corr-v2" {
		t.Fatalf("output = %#v", out)
	}
	if stub.req.ContractVersion != domain.V2ContractVersion || len(stub.req.Evidence) != 1 {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.Evidence[0].Content != "Dense-Mem uses PostgreSQL." {
		t.Fatalf("evidence content = %q", stub.req.Evidence[0].Content)
	}
	if len(stub.req.EntityHints) != 2 || stub.req.EntityHints[0]["ref"] != "entity:dense-mem" {
		t.Fatalf("entity hints = %#v", stub.req.EntityHints)
	}
	if len(stub.req.RelationshipHints) != 1 || stub.req.RelationshipHints[0]["proposal_id"] != "rel:uses" {
		t.Fatalf("relationship hints = %#v", stub.req.RelationshipHints)
	}
	target, ok := stub.req.RelationshipHints[0]["correction_target"].(map[string]any)
	if !ok {
		t.Fatalf("correction target hint = %#v", stub.req.RelationshipHints[0]["correction_target"])
	}
	version, versionOK := schemaNumber(target["expected_version"])
	if target["relationship_id"] != correctionTargetID || !versionOK || int(version) != 2 {
		t.Fatalf("correction target hint = %#v", stub.req.RelationshipHints[0]["correction_target"])
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
	_, err = remember.Invoke(v2ContractInvokeContext("write"), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("remember.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATRememberRejectsReadOnlyCredential(t *testing.T) {
	stub := &stubV2RememberService{}
	reg, err := BuildV2UAT(Dependencies{V2Remember: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	remember, ok := reg.Get(V2ToolRemember)
	if !ok {
		t.Fatal("BuildV2UAT did not register remember")
	}
	_, err = remember.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing required scope") {
		t.Fatalf("remember.Invoke err = %v, want missing scope rejection", err)
	}
	if len(stub.req.Evidence) != 0 {
		t.Fatalf("remember service was called with read-only scopes: %#v", stub.req)
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
	out, err := recall.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"query": "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("recall_memory.Invoke: %v", err)
	}
	if out["recall_id"] != "rec-v2" {
		t.Fatalf("recall_id = %#v, want rec-v2", out["recall_id"])
	}
	if err := ValidateInput(Tool{InputSchema: recall.OutputSchema}, out); err != nil {
		t.Fatalf("recall output contract validation failed: %v; output = %#v", err, out)
	}
	for _, forbidden := range []string{"search_state", "degradation"} {
		if _, ok := out[forbidden]; ok {
			t.Fatalf("recall_memory exposed %s in public output: %#v", forbidden, out)
		}
	}
	results, ok := out["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v, want one result", out["results"])
	}
	result := results[0]
	for _, forbidden := range []string{"score", "rank", "relationship_ids"} {
		if _, ok := result[forbidden]; ok {
			t.Fatalf("recall_memory exposed %s in public output: %#v", forbidden, result)
		}
	}
	if result["evidence_id"] != "evidence-v2" || result["context"] != "Dense-Mem uses PostgreSQL." {
		t.Fatalf("result = %#v, want public evidence context", result)
	}
	if _, ok := out["related_hypotheses"].([]memoryservice.V2RelatedHypothesisSummary); !ok {
		t.Fatalf("related_hypotheses = %#v, want typed empty array", out["related_hypotheses"])
	}
	if _, ok := out["discovery_paths"].([]memoryservice.V2RecallDiscoveryPath); !ok {
		t.Fatalf("discovery_paths = %#v, want typed empty array", out["discovery_paths"])
	}
	if _, ok := out["conflicts"].([]memoryservice.V2RecallConflictSummary); !ok {
		t.Fatalf("conflicts = %#v, want typed empty array", out["conflicts"])
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
	_, err = recall.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"query":   "PostgreSQL memory",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("recall_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATRecallRejectsMissingReadScope(t *testing.T) {
	stub := &stubV2RecallService{}
	reg, err := BuildV2UAT(Dependencies{V2Recall: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	recall, ok := reg.Get(V2ToolRecallMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register recall_memory")
	}
	_, err = recall.Invoke(context.Background(), "ignored-profile", map[string]any{
		"query": "PostgreSQL memory",
	})
	if err == nil || !strings.Contains(err.Error(), "missing required scope") {
		t.Fatalf("recall_memory.Invoke err = %v, want missing scope rejection", err)
	}
	if stub.req.Query != "" {
		t.Fatalf("recall service was called without read scope: %#v", stub.req)
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
	out, err := trace.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
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
	if err := ValidateInput(Tool{InputSchema: trace.OutputSchema}, out); err != nil {
		t.Fatalf("trace output contract validation failed: %v; output = %#v", err, out)
	}
	for _, forbidden := range []string{"search_documents", "embedding_jobs", "truncated"} {
		if _, ok := out[forbidden]; ok {
			t.Fatalf("trace_memory exposed internal field %s in public output: %#v", forbidden, out)
		}
	}
	for _, forbidden := range []string{"team_id", "semantic_group_key", "status"} {
		if _, ok := relationship[forbidden]; ok {
			t.Fatalf("trace_memory relationship exposed internal field %s: %#v", forbidden, relationship)
		}
	}
	if relationship["relationship_status"] != string(domain.V2RelationshipStatusActive) {
		t.Fatalf("relationship_status = %#v, want active", relationship["relationship_status"])
	}
	supports, ok := out["evidence_supports"].([]map[string]any)
	if !ok || len(supports) != 1 {
		t.Fatalf("evidence_supports = %#v, want one public support", out["evidence_supports"])
	}
	if supports[0]["span_start"] != 0 || supports[0]["span_end"] != 12 {
		t.Fatalf("support spans = %#v, want zero-based span retained", supports[0])
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
	_, err = trace.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id":         "attacker-team",
		"relationship_id": "relationship-v2",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("trace_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildV2UATTraceRejectsMissingReadScope(t *testing.T) {
	stub := &stubV2TraceContext{}
	reg, err := BuildV2UAT(Dependencies{Context: stub})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	trace, ok := reg.Get(V2ToolTraceMemory)
	if !ok {
		t.Fatal("BuildV2UAT did not register trace_memory")
	}
	_, err = trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"relationship_id": "relationship-v2",
	})
	if err == nil || !strings.Contains(err.Error(), "missing required scope") {
		t.Fatalf("trace_memory.Invoke err = %v, want missing scope rejection", err)
	}
	if stub.req.RelationshipID != "" {
		t.Fatalf("trace service was called without read scope: %#v", stub.req)
	}
}
