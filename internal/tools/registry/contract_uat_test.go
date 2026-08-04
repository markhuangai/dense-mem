package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildActiveWiresExecutableRemember(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{Remember: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	correctionTargetID := uuid.NewString()
	remember, ok := reg.Get(ToolRemember)
	if !ok {
		t.Fatal("BuildActive did not register remember")
	}
	if remember.Invoke == nil {
		t.Fatal("BuildActive remember invoker is nil")
	}
	input := validFlatRelationshipSubmission()
	input["evidence"].([]any)[0].(map[string]any)["idempotency_key"] = "eval:doc-alpha"
	relationship(input)["ref"] = "rel:uses"
	relationship(input)["correction_target"] = map[string]any{
		"relationship_id":  correctionTargetID,
		"expected_version": 2,
	}
	out, err := remember.Invoke(contractInvokeContext("write"), "ignored-profile", input)
	if err != nil {
		t.Fatalf("remember.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: remember.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["ingest_id"] != "ingest-canonical" {
		t.Fatalf("ingest_id = %#v, want ingest-canonical", out["ingest_id"])
	}
	if out["processing_state"] != string(domain.PlacementRunQueued) || out["correlation_id"] != "corr-canonical" {
		t.Fatalf("output = %#v", out)
	}
	if stub.req.ContractVersion != domain.ContractVersion || len(stub.req.Evidence) != 1 {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.IdempotencyKey != "eval:doc-alpha" {
		t.Fatalf("remember idempotency key = %q", stub.req.IdempotencyKey)
	}
	if stub.req.Evidence[0].Content != "Dense-Mem uses PostgreSQL." {
		t.Fatalf("evidence content = %q", stub.req.Evidence[0].Content)
	}
	if len(stub.req.EntityHints) != 0 {
		t.Fatalf("entity hints = %#v", stub.req.EntityHints)
	}
	if len(stub.req.RelationshipHints) != 1 || stub.req.RelationshipHints[0]["ref"] != "rel:uses" {
		t.Fatalf("relationship hints = %#v", stub.req.RelationshipHints)
	}
	predicate, ok := stub.req.RelationshipHints[0]["predicate"].(map[string]any)
	if !ok || predicate["proposed_key"] != "uses" {
		t.Fatalf("predicate hint = %#v", stub.req.RelationshipHints[0]["predicate"])
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

func TestBuildActiveRememberRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Remember: &stubRememberService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	remember, ok := reg.Get(ToolRemember)
	if !ok {
		t.Fatal("BuildActive did not register remember")
	}
	_, err = remember.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("remember.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveRememberRejectsReadOnlyCredential(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{Remember: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	remember, ok := reg.Get(ToolRemember)
	if !ok {
		t.Fatal("BuildActive did not register remember")
	}
	_, err = remember.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
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

func TestBuildActiveWiresExecutableRecallMemory(t *testing.T) {
	stub := &stubRecallService{}
	reg, err := BuildActive(Dependencies{Recall: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	recall, ok := reg.Get(ToolRecallMemory)
	if !ok {
		t.Fatal("BuildActive did not register recall_memory")
	}
	if recall.Invoke == nil {
		t.Fatal("BuildActive recall_memory invoker is nil")
	}
	out, err := recall.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"query": "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("recall_memory.Invoke: %v", err)
	}
	if out["recall_id"] != "rec-canonical" {
		t.Fatalf("recall_id = %#v, want rec-canonical", out["recall_id"])
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
	if result["evidence_id"] != "evidence-canonical" || result["context"] != "Dense-Mem uses PostgreSQL." {
		t.Fatalf("result = %#v, want public evidence context", result)
	}
	if _, ok := out["related_hypotheses"].([]memoryservice.RelatedHypothesisSummary); !ok {
		t.Fatalf("related_hypotheses = %#v, want typed empty array", out["related_hypotheses"])
	}
	if _, ok := out["related_relationships"].([]memoryservice.RelatedRelationshipSummary); !ok {
		t.Fatalf("related_relationships = %#v, want typed empty array", out["related_relationships"])
	}
	if _, ok := out["related_communities"].([]memoryservice.RecallDiscoveryPath); !ok {
		t.Fatalf("related_communities = %#v, want typed empty array", out["related_communities"])
	}
	if _, ok := out["conflicts"].([]memoryservice.RecallConflictSummary); !ok {
		t.Fatalf("conflicts = %#v, want typed empty array", out["conflicts"])
	}
	if stub.req.Query != "PostgreSQL memory" {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.ContractVersion != domain.ContractVersion {
		t.Fatalf("contract version = %q", stub.req.ContractVersion)
	}
}

func TestBuildActiveRecallRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Recall: &stubRecallService{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	recall, ok := reg.Get(ToolRecallMemory)
	if !ok {
		t.Fatal("BuildActive did not register recall_memory")
	}
	_, err = recall.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"query":   "PostgreSQL memory",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("recall_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveRecallRejectsMissingReadScope(t *testing.T) {
	stub := &stubRecallService{}
	reg, err := BuildActive(Dependencies{Recall: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	recall, ok := reg.Get(ToolRecallMemory)
	if !ok {
		t.Fatal("BuildActive did not register recall_memory")
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

func TestBuildActiveWiresExecutableTraceMemory(t *testing.T) {
	stub := &stubTraceContext{}
	reg, err := BuildActive(Dependencies{Context: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	trace, ok := reg.Get(ToolTraceMemory)
	if !ok {
		t.Fatal("BuildActive did not register trace_memory")
	}
	if trace.Invoke == nil {
		t.Fatal("BuildActive trace_memory invoker is nil")
	}
	out, err := trace.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"relationship_id":          "relationship-canonical",
		"include_evidence_content": false,
		"include_verification":     true,
		"include_transitions":      false,
		"max_depth":                2,
		"max_edges":                12,
		"predicate_keys":           []any{"works_on"},
		"topic":                    "PostgreSQL memory",
		"min_relevance":            0.7,
	})
	if err != nil {
		t.Fatalf("trace_memory.Invoke: %v", err)
	}
	relationship, ok := out["relationship"].(map[string]any)
	if !ok || relationship["relationship_id"] != "relationship-canonical" {
		t.Fatalf("relationship = %#v", out["relationship"])
	}
	if _, ok := out["legacy_semantic"]; ok {
		t.Fatalf("trace_memory should unwrap payload, got %#v", out)
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
	if relationship["relationship_status"] != string(domain.RelationshipStatusActive) {
		t.Fatalf("relationship_status = %#v, want active", relationship["relationship_status"])
	}
	supports, ok := out["evidence_supports"].([]map[string]any)
	if !ok || len(supports) != 1 {
		t.Fatalf("evidence_supports = %#v, want one public support", out["evidence_supports"])
	}
	if supports[0]["span_start"] != 0 || supports[0]["span_end"] != 12 {
		t.Fatalf("support spans = %#v, want zero-based span retained", supports[0])
	}
	if stub.req.RelationshipID != "relationship-canonical" || stub.req.MaxDepth != 2 || stub.req.MaxEdges != 12 {
		t.Fatalf("trace request = %#v", stub.req)
	}
	if got := strings.Join(stub.req.PredicateKeys, ","); got != "works_on" {
		t.Fatalf("predicate_keys = %q", got)
	}
	if stub.req.MinRelevance == nil || *stub.req.MinRelevance != 0.7 {
		t.Fatalf("min_relevance = %#v, want 0.7", stub.req.MinRelevance)
	}
}

func TestBuildActiveTraceRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{Context: &stubTraceContext{}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	trace, ok := reg.Get(ToolTraceMemory)
	if !ok {
		t.Fatal("BuildActive did not register trace_memory")
	}
	_, err = trace.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id":         "attacker-team",
		"relationship_id": "relationship-canonical",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("trace_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveTraceRejectsMissingReadScope(t *testing.T) {
	stub := &stubTraceContext{}
	reg, err := BuildActive(Dependencies{Context: stub})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	trace, ok := reg.Get(ToolTraceMemory)
	if !ok {
		t.Fatal("BuildActive did not register trace_memory")
	}
	_, err = trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"relationship_id": "relationship-canonical",
	})
	if err == nil || !strings.Contains(err.Error(), "missing required scope") {
		t.Fatalf("trace_memory.Invoke err = %v, want missing scope rejection", err)
	}
	if stub.req.RelationshipID != "" {
		t.Fatalf("trace service was called without read scope: %#v", stub.req)
	}
}
