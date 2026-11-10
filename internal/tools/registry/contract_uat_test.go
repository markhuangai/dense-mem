package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"

	recallapp "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestBuildActiveWiresExecutableRemember(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{RememberBindings: RememberBindings{Service: stub}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	remember, ok := reg.Get(ToolRemember)
	if !ok {
		t.Fatal("BuildActive did not register remember")
	}
	if remember.Invoke == nil {
		t.Fatal("BuildActive remember invoker is nil")
	}
	input := validEvidenceOnlySubmission()
	input["idempotency_key"] = "eval:doc-alpha"
	out, err := remember.Invoke(contractInvokeContext("read", "write"), "ignored-profile", input)
	if err != nil {
		t.Fatalf("remember.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: remember.OutputSchema}, out); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	if out["submission_id"] != "ingest-canonical" {
		t.Fatalf("submission_id = %#v, want ingest-canonical", out["submission_id"])
	}
	if out["processing_state"] != "completed" || out["correlation_id"] != "corr-canonical" {
		t.Fatalf("output = %#v", out)
	}
	if len(stub.req.Evidence) != 1 {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
	if stub.req.IdempotencyKey != "eval:doc-alpha" {
		t.Fatalf("remember idempotency key = %q", stub.req.IdempotencyKey)
	}
	if stub.req.Evidence[0].Content != "Dense-Mem uses PostgreSQL." {
		t.Fatalf("evidence content = %q", stub.req.Evidence[0].Content)
	}

}

func TestBuildActiveRememberCapturesCallerEnvelope(t *testing.T) {
	var requestBody []byte
	var callerResponse []byte
	stub := &stubRememberService{inspectContext: func(ctx context.Context) {
		capture := rememberapp.DiagnosticCaptureFromContext(ctx)
		require.NotNil(t, capture)
		requestBody = capture.RequestBody()
		var err error
		callerResponse, err = capture.ProjectResponse(map[string]any{"code": "provider_unavailable"}, true)
		require.NoError(t, err)
	}}
	reg, err := BuildActive(Dependencies{RememberBindings: RememberBindings{Service: stub}})
	require.NoError(t, err)
	remember, ok := reg.Get(ToolRemember)
	require.True(t, ok)
	input := validEvidenceOnlySubmission()
	input["idempotency_key"] = "diagnostic-capture"
	_, err = remember.Invoke(contractInvokeContext("read", "write"), "ignored-profile", input)
	require.NoError(t, err)
	require.Contains(t, string(requestBody), `"name":"remember"`)
	require.Contains(t, string(callerResponse), `"isError":true`)
	require.Contains(t, string(callerResponse), `"structuredContent"`)
}

func TestBuildActiveRememberRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{RememberBindings: RememberBindings{Service: &stubRememberService{}}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	remember, ok := reg.Get(ToolRemember)
	if !ok {
		t.Fatal("BuildActive did not register remember")
	}
	_, err = remember.Invoke(contractInvokeContext("read", "write"), "ignored-profile", map[string]any{
		"team_id": "attacker-team",
		"evidence": []any{
			map[string]any{"content": "remember this exact evidence"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("remember.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveRememberReturnsTypedDynamicValidation(t *testing.T) {
	stub := &stubRememberService{err: &rememberapp.RememberValidationError{
		Issues: []rememberapp.RememberValidationIssue{{
			Path: "/relationships/0/predicate/known_predicate_key", Code: "unavailable", Message: "known_predicate_key is unavailable",
		}},
	}}
	reg, err := BuildActive(Dependencies{RememberBindings: RememberBindings{Service: stub}})
	require.NoError(t, err)
	remember, ok := reg.Get(ToolRemember)
	require.True(t, ok)
	_, err = remember.Invoke(contractInvokeContext("read", "write"), "ignored-profile", validEvidenceOnlySubmission())
	result, ok := ContractValidationResultFromError(err)
	require.True(t, ok)
	require.Equal(t, []ContractValidationIssue{{
		Path: "/relationships/0/predicate/known_predicate_key", Code: "unavailable", Message: "known_predicate_key is unavailable",
	}}, result.Issues)
}

func TestBuildActiveRememberAllowsTeamReadOnlyPrivateSubmission(t *testing.T) {
	stub := &stubRememberService{}
	reg, err := BuildActive(Dependencies{RememberBindings: RememberBindings{Service: stub}})
	require.NoError(t, err)
	tool, ok := reg.Get(ToolRemember)
	require.True(t, ok)
	input := validEvidenceOnlySubmission()
	input["target_space"] = "private"
	_, err = tool.Invoke(contractInvokeContext("read"), "ignored-profile", input)
	require.NoError(t, err)
	require.Equal(t, domain.MemorySpacePrivate, stub.req.TargetSpace)
	require.Len(t, stub.req.Evidence, 1)
}

func TestBuildActiveWiresExecutableRecallMemory(t *testing.T) {
	stub := &stubRecallService{}
	reg, err := BuildActive(Dependencies{RecallBindings: RecallBindings{Service: stub}})
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
	if result["evidence_id"] != "evidence-canonical" || result["context"] != "Dense-Mem uses PostgreSQL." || result["space_kind"] != string(domain.MemorySpaceTeamShared) {
		t.Fatalf("result = %#v, want public evidence context", result)
	}
	if _, ok := out["related_hypotheses"]; ok {
		t.Fatal("recall exposed retired hypotheses")
	}
	if _, ok := out["related_topics"].([]recallcontract.RelatedTopic); !ok {
		t.Fatalf("related_topics = %#v", out["related_topics"])
	}
	if _, ok := out["topics_truncated"].(bool); !ok {
		t.Fatalf("topics_truncated = %#v", out["topics_truncated"])
	}
	if _, ok := out["conflicts"].([]recallapp.RecallConflictSummary); !ok {
		t.Fatalf("conflicts = %#v, want typed empty array", out["conflicts"])
	}
	if stub.req.Query != "PostgreSQL memory" {
		t.Fatalf("stub request not populated: %#v", stub.req)
	}
}

func TestBuildActiveRecallRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{RecallBindings: RecallBindings{Service: &stubRecallService{}}})
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
	reg, err := BuildActive(Dependencies{RecallBindings: RecallBindings{Service: stub}})
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

func TestEvidenceOnlyCatalogWiresExecutableTraceMemory(t *testing.T) {
	stub := &stubTraceContext{}
	reg, err := BuildActive(Dependencies{TraceBindings: TraceBindings{Service: stub}})
	require.NoError(t, err)
	tool, ok := reg.Get(ToolTraceMemory)
	require.True(t, ok)
	out, err := tool.Invoke(contractInvokeContext("read"), "ignored", map[string]any{"evidence_id": "00000000-0000-0000-0000-000000000001", "include_evidence_content": false, "include_transitions": false, "max_events": 12, "max_fragment_content_runes": 200})
	require.NoError(t, err)
	evidence, ok := out["evidence"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "evidence-canonical", evidence["evidence_id"])
	require.NotContains(t, evidence, "team_id")
	require.Equal(t, true, out["truncated"])
	require.NoError(t, ValidateInput(Tool{InputSchema: tool.OutputSchema}, out))
	require.Equal(t, 12, stub.req.MaxEvents)
	require.Equal(t, 200, stub.req.MaxFragmentContentRunes)
	require.NotNil(t, stub.req.IncludeEvidenceContent)
	require.False(t, *stub.req.IncludeEvidenceContent)
	require.NotNil(t, stub.req.IncludeTransitions)
	require.False(t, *stub.req.IncludeTransitions)
}

func TestBuildActiveTraceRejectsTenantOverride(t *testing.T) {
	reg, err := BuildActive(Dependencies{TraceBindings: TraceBindings{Service: &stubTraceContext{}}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	trace, ok := reg.Get(ToolTraceMemory)
	if !ok {
		t.Fatal("BuildActive did not register trace_memory")
	}
	_, err = trace.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
		"team_id":     "attacker-team",
		"evidence_id": "00000000-0000-0000-0000-000000000001",
	})
	if err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("trace_memory.Invoke err = %v, want tenant override rejection", err)
	}
}

func TestBuildActiveTraceRejectsMissingReadScope(t *testing.T) {
	stub := &stubTraceContext{}
	reg, err := BuildActive(Dependencies{TraceBindings: TraceBindings{Service: stub}})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	trace, ok := reg.Get(ToolTraceMemory)
	if !ok {
		t.Fatal("BuildActive did not register trace_memory")
	}
	_, err = trace.Invoke(context.Background(), "ignored-profile", map[string]any{
		"evidence_id": "00000000-0000-0000-0000-000000000001",
	})
	if err == nil || !strings.Contains(err.Error(), "missing required scope") {
		t.Fatalf("trace_memory.Invoke err = %v, want missing scope rejection", err)
	}
	if stub.req.EvidenceID != "" {
		t.Fatalf("trace service was called without read scope: %#v", stub.req)
	}
}
