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
			name: ToolRemember,
			args: map[string]any{
				"evidence": []any{map[string]any{"content": "remember"}},
			},
		},
		{
			name: ToolRecallMemory,
			args: map[string]any{
				"query": "PostgreSQL",
			},
		},
		{
			name: ToolGetSubmissionStatus,
			args: map[string]any{
				"submission_id": "submission-canonical",
			},
		},
		{
			name: ToolCorrectRelationship,
			args: map[string]any{
				"action":           "submit",
				"relationship_id":  "relationship-source",
				"expected_version": 1,
				"patch":            map[string]any{"predicate": map[string]any{"key": "works_with"}},
				"supports":         []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 8}},
				"reason":           "predicate resolved incorrectly",
				"idempotency_key":  "correction-1",
			},
		},
		{
			name: ToolTraceMemory,
			args: map[string]any{
				"relationship_id": "relationship-canonical",
			},
		},
		{
			name: ToolListDreams,
			args: map[string]any{
				"status": "proposed",
			},
		},
		{
			name: ToolGetDream,
			args: map[string]any{
				"hypothesis_id": "dream-v2",
			},
		},
		{
			name: ToolResolveDreamFeedback,
			args: map[string]any{
				"hypothesis_id": "dream-v2",
				"decision":      "confirm_true",
				"evidence":      []any{map[string]any{"content": "Independent evidence."}},
			},
		},
		{
			name: ToolExportMemoryPack,
			args: map[string]any{
				"name":             "PostgreSQL pack",
				"relationship_ids": []any{"relationship-canonical"},
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

func TestBuildActiveWiresExecutableDreamTools(t *testing.T) {
	dreams := &stubDreamService{}
	reg, err := BuildActive(Dependencies{Dreams: dreams})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}

	list, ok := reg.Get(ToolListDreams)
	if !ok || list.Invoke == nil {
		t.Fatal("BuildActive did not register executable list_dreams")
	}
	listOut, err := list.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
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

	get, ok := reg.Get(ToolGetDream)
	if !ok || get.Invoke == nil {
		t.Fatal("BuildActive did not register executable get_dream")
	}
	getOut, err := get.Invoke(contractInvokeContext("read"), "ignored-profile", map[string]any{
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

	resolve, ok := reg.Get(ToolResolveDreamFeedback)
	if !ok || resolve.Invoke == nil {
		t.Fatal("BuildActive did not register executable resolve_dream_feedback")
	}
	_, err = resolve.Invoke(contractInvokeContext("write"), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
		"decision":      "confirm_true",
	})
	if err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("resolve_dream_feedback missing evidence err = %v", err)
	}
	resolveInput := validFlatRelationshipSubmission()
	delete(resolveInput, "idempotency_key")
	relationship(resolveInput)["modality"] = "statement"
	resolveInput["hypothesis_id"] = "dream-v2"
	resolveInput["decision"] = "confirm_true"
	resolveOut, err := resolve.Invoke(contractInvokeContext("write"), "ignored-profile", resolveInput)
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
		dreams.lastResolveReq.Evidence[0].Content != "Dense-Mem uses PostgreSQL." {
		t.Fatalf("resolve_dream_feedback evidence = %#v", dreams.lastResolveReq.Evidence)
	}
	if len(dreams.lastResolveReq.RelationshipHints) != 1 || dreams.lastResolveReq.RelationshipHints[0]["ref"] != "uses-postgresql" {
		t.Fatalf("resolve_dream_feedback relationships = %#v", dreams.lastResolveReq.RelationshipHints)
	}
	if dreams.lastResolveReq.DreamID != "dream-v2" {
		t.Fatalf("resolve_dream_feedback dream id = %q", dreams.lastResolveReq.DreamID)
	}
}
