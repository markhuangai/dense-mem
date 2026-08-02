package registry

import (
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestBuildActiveDreamToolsInvokeAndValidate(t *testing.T) {
	dreams := &stubDreamService{}
	reg, err := BuildActive(Dependencies{Dreams: dreams})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}

	listTool, ok := reg.Get("list_dreams")
	if !ok {
		t.Fatal("list_dreams not registered")
	}
	statusEnums := listTool.InputSchema["properties"].(map[string]any)["status"].(map[string]any)["enum"].([]string)
	for _, status := range domain.HypothesisStatuses() {
		if !containsString(statusEnums, status) {
			t.Fatalf("list_dreams status enum missing %q: %v", status, statusEnums)
		}
	}
	if containsString(statusEnums, "promoted") {
		t.Fatalf("list_dreams status enum contains legacy promoted status: %v", statusEnums)
	}
	ctx := contractInvokeContext("read", "write")
	listOut, err := listTool.Invoke(ctx, "profile-dream", map[string]any{
		"limit":  float64(3),
		"status": string(domain.HypothesisReinforced),
	})
	if err != nil {
		t.Fatalf("list_dreams Invoke: %v", err)
	}
	listed := listOut["dreams"].([]map[string]any)
	if len(listed) != 1 || dreams.lastListOpts.Limit != 3 || dreams.lastListOpts.Status != string(domain.HypothesisReinforced) {
		t.Fatalf("list_dreams output = %v opts = %+v", listOut, dreams.lastListOpts)
	}

	getTool, _ := reg.Get("get_dream")
	getOut, err := getTool.Invoke(ctx, "profile-dream", map[string]any{"hypothesis_id": "dream-1"})
	if err != nil {
		t.Fatalf("get_dream Invoke: %v", err)
	}
	hypothesis, ok := getOut["hypothesis"].(map[string]any)
	if !ok || hypothesis["hypothesis_id"] != "dream-1" {
		t.Fatalf("get_dream hypothesis = %v, want dream-1", getOut["hypothesis"])
	}
	derivations, ok := hypothesis["derivations"].([]map[string]any)
	if !ok || len(derivations) != 1 || derivations[0]["quote"] != "A affects B." {
		t.Fatalf("get_dream derivations = %#v", hypothesis["derivations"])
	}
	if _, err := getTool.Invoke(ctx, "profile-dream", map[string]any{}); err == nil || !strings.Contains(err.Error(), "hypothesis_id is required") {
		t.Fatalf("get_dream missing id err = %v", err)
	}

	resolveTool, _ := reg.Get("resolve_dream_feedback")
	decisionEnums := resolveTool.InputSchema["properties"].(map[string]any)["decision"].(map[string]any)["enum"].([]string)
	for _, decision := range []string{"reinforce", "stale", "reject", "confirm_true", "confirm_false"} {
		if !containsString(decisionEnums, decision) {
			t.Fatalf("resolve_dream_feedback decision enum missing %q: %v", decision, decisionEnums)
		}
	}
	for _, legacy := range []string{"ignore", "promote_candidate"} {
		if containsString(decisionEnums, legacy) {
			t.Fatalf("resolve_dream_feedback decision enum contains legacy %q: %v", legacy, decisionEnums)
		}
	}
	resolveOut, err := resolveTool.Invoke(ctx, "profile-dream", map[string]any{
		"hypothesis_id": "dream-1",
		"decision":      "reinforce",
		"reason":        "prior conversation still makes this useful",
	})
	if err != nil {
		t.Fatalf("resolve_dream_feedback Invoke: %v", err)
	}
	if resolveOut["hypothesis_id"] != "dream-1" || dreams.lastResolveReq.Decision != "reinforce" || dreams.lastResolveReq.Feedback != "prior conversation still makes this useful" {
		t.Fatalf("resolve_dream_feedback output = %v request = %+v", resolveOut, dreams.lastResolveReq)
	}
}

func TestResolveDreamFeedbackInputSchemaIsDecisionConditional(t *testing.T) {
	schema := resolveDreamFeedbackInputSchema()
	tool := Tool{InputSchema: schema}

	if err := ValidateInput(tool, map[string]any{
		"hypothesis_id": "dream-1",
		"decision":      "confirm_true",
	}); err == nil || !strings.Contains(err.Error(), "evidence is required and proposal is required") {
		t.Fatalf("confirmation without submission input error = %v", err)
	}

	if err := ValidateInput(tool, map[string]any{
		"hypothesis_id": "dream-1",
		"decision":      "reject",
		"reason":        "The premise is no longer applicable.",
	}); err != nil {
		t.Fatalf("lifecycle feedback input validation: %v", err)
	}

	if err := ValidateInput(tool, map[string]any{
		"hypothesis_id": "dream-1",
		"decision":      "confirm_false",
		"evidence": []any{
			map[string]any{"content": "Dense-Mem uses PostgreSQL."},
		},
		"proposal": validSpanGroundedProposal(),
	}); err != nil {
		t.Fatalf("confirmation feedback input validation: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
