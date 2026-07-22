package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestBuildActiveExecutableToolsRequireDependencies(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
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
				"message":         "forget this relationship",
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
		{
			name: V2ToolListDreams,
			args: map[string]any{
				"status": "proposed",
			},
		},
		{
			name: V2ToolGetDream,
			args: map[string]any{
				"hypothesis_id": "dream-v2",
			},
		},
		{
			name: V2ToolResolveDreamFeedback,
			args: map[string]any{
				"hypothesis_id": "dream-v2",
				"decision":      "confirm_true",
				"evidence":      []any{map[string]any{"content": "Independent evidence."}},
			},
		},
		{
			name: V2ToolFindMemoryPackCandidates,
			args: map[string]any{
				"query": "PostgreSQL",
			},
		},
		{
			name: V2ToolExportMemoryPack,
			args: map[string]any{
				"name":             "PostgreSQL pack",
				"relationship_ids": []any{"relationship-v2"},
			},
		},
		{
			name: V2ToolInspectMemoryPack,
			args: map[string]any{
				"artifact_json": "{}",
				"mode":          "review",
			},
		},
		{
			name: V2ToolImportMemoryPack,
			args: map[string]any{
				"artifact_json": "{}",
				"mode":          "review",
			},
		},
		{
			name: V2ToolRollbackMemoryPackImport,
			args: map[string]any{
				"import_id": "import-v2",
				"dry_run":   true,
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

func TestBuildActiveWiresEvaluationRecallCaseToV2(t *testing.T) {
	recall := &stubRecallService{}
	reg, err := BuildActive(Dependencies{
		Recall:          recall,
		EvaluationAudit: &evaluationAuditStub{},
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	tool, ok := reg.Get("eval_run_recall_case")
	if !ok || tool.Invoke == nil {
		t.Fatal("BuildActive did not register executable eval_run_recall_case")
	}
	out, err := tool.Invoke(v2ContractInvokeContext("read", "write"), "ignored-profile", map[string]any{
		"case_id":                "case-v2",
		"query":                  "PostgreSQL memory",
		"limit":                  float64(3),
		"known_evidence_ids":     []any{"evidence-known"},
		"known_relationship_ids": []any{"relationship-known"},
		"expand_from_entity_ids": []any{"entity-expand"},
	})
	if err != nil {
		t.Fatalf("eval_run_recall_case.Invoke: %v", err)
	}
	if recall.req.Query != "PostgreSQL memory" || recall.req.Limit != 3 {
		t.Fatalf("V2 recall request = %#v", recall.req)
	}
	if len(recall.req.KnownEvidenceIDs) != 1 || recall.req.KnownEvidenceIDs[0] != "evidence-known" {
		t.Fatalf("known evidence ids = %#v", recall.req.KnownEvidenceIDs)
	}
	if len(recall.req.KnownRelationshipIDs) != 1 || recall.req.KnownRelationshipIDs[0] != "relationship-known" {
		t.Fatalf("known relationship ids = %#v", recall.req.KnownRelationshipIDs)
	}
	if len(recall.req.ExpandFromEntityIDs) != 1 || recall.req.ExpandFromEntityIDs[0] != "entity-expand" {
		t.Fatalf("expand entity ids = %#v", recall.req.ExpandFromEntityIDs)
	}
	ranked, ok := out["ranked_refs"].([]map[string]any)
	if !ok || len(ranked) != 1 || ranked[0]["type"] != "evidence" || ranked[0]["id"] != "evidence-v2" {
		t.Fatalf("ranked refs = %#v", out["ranked_refs"])
	}
	if out["search_state"] != string(domain.V2SearchProjectionCurrent) {
		t.Fatalf("search_state = %#v", out["search_state"])
	}
}

func TestBuildActiveWiresExecutableDreamTools(t *testing.T) {
	dreams := &stubDreamService{}
	reg, err := BuildActive(Dependencies{Dreams: dreams})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}

	list, ok := reg.Get(V2ToolListDreams)
	if !ok || list.Invoke == nil {
		t.Fatal("BuildActive did not register executable list_dreams")
	}
	listOut, err := list.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"limit":  float64(2),
		"status": "submitted",
	})
	if err != nil {
		t.Fatalf("list_dreams.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: list.OutputSchema}, listOut); err != nil {
		t.Fatalf("list_dreams output validation failed: %v; output = %#v", err, listOut)
	}
	if listOut["dreams"] == nil || dreams.lastListOpts.Limit != 2 || dreams.lastListOpts.Status != "submitted" {
		t.Fatalf("list_dreams output = %#v opts = %#v", listOut, dreams.lastListOpts)
	}

	get, ok := reg.Get(V2ToolGetDream)
	if !ok || get.Invoke == nil {
		t.Fatal("BuildActive did not register executable get_dream")
	}
	getOut, err := get.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
	})
	if err != nil {
		t.Fatalf("get_dream.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: get.OutputSchema}, getOut); err != nil {
		t.Fatalf("get_dream output validation failed: %v; output = %#v", err, getOut)
	}
	hypothesis, ok := getOut["hypothesis"].(map[string]any)
	if !ok || hypothesis["hypothesis_id"] != "dream-v2" {
		t.Fatalf("get_dream hypothesis = %#v", getOut["hypothesis"])
	}

	resolve, ok := reg.Get(V2ToolResolveDreamFeedback)
	if !ok || resolve.Invoke == nil {
		t.Fatal("BuildActive did not register executable resolve_dream_feedback")
	}
	_, err = resolve.Invoke(v2ContractInvokeContext("write"), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
		"decision":      "confirm_true",
	})
	if err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("resolve_dream_feedback missing evidence err = %v", err)
	}
	resolveOut, err := resolve.Invoke(v2ContractInvokeContext("write"), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
		"decision":      "confirm_true",
		"evidence": []any{
			map[string]any{"content": "A deployment note independently confirms Dense-Mem uses PostgreSQL."},
		},
	})
	if err != nil {
		t.Fatalf("resolve_dream_feedback.Invoke: %v", err)
	}
	if err := ValidateInput(Tool{InputSchema: resolve.OutputSchema}, resolveOut); err != nil {
		t.Fatalf("resolve_dream_feedback output validation failed: %v; output = %#v", err, resolveOut)
	}
	if resolveOut["hypothesis_id"] != "dream-1" || resolveOut["status"] != string(domain.DreamStatusProposed) ||
		dreams.lastResolveReq.Decision != "confirm_true" {
		t.Fatalf("resolve_dream_feedback output = %#v request = %#v", resolveOut, dreams.lastResolveReq)
	}
	if len(dreams.lastResolveReq.Evidence) != 1 ||
		dreams.lastResolveReq.Evidence[0].Content != "A deployment note independently confirms Dense-Mem uses PostgreSQL." {
		t.Fatalf("resolve_dream_feedback evidence = %#v", dreams.lastResolveReq.Evidence)
	}
	if dreams.lastResolveReq.DreamID != "dream-v2" {
		t.Fatalf("resolve_dream_feedback dream id = %q", dreams.lastResolveReq.DreamID)
	}
}
