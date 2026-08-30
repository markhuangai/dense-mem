package registry

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/stretchr/testify/require"
)

func TestResolveDreamFeedbackReturnsStructuredTerminalFailure(t *testing.T) {
	dreams := &stubDreamService{resolveResult: &dreamservice.ResolveFeedbackResult{
		Dream: stubDream("profile-dream"),
		Memory: &rememberapp.RememberResult{Terminal: &rememberapp.TerminalRememberResult{
			ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
			ProcessingState: string(rememberapp.TerminalProcessingFailed), SearchState: string(rememberapp.TerminalSearchNotRequired),
			CorrelationID: "terminal-failure", Evidence: []rememberapp.TerminalEvidenceResult{},
			RelationshipResults: []rememberapp.SubmissionRelationshipResult{},
			Errors:              []rememberapp.SubmissionStatusError{rememberapp.StatusError(rememberapp.SubmissionErrorProviderUnavailable)},
		}},
	}}
	reg, err := BuildActive(Dependencies{Dreams: dreams})
	require.NoError(t, err)
	tool, ok := reg.Get(ToolResolveDreamFeedback)
	require.True(t, ok)

	_, err = tool.Invoke(contractInvokeContext("write"), "profile-dream", map[string]any{
		"hypothesis_id": "dream-1", "decision": "confirm_true",
		"evidence": []any{map[string]any{"content": "independent evidence"}},
		"relationships": []any{map[string]any{
			"ref": "r-1", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
			"polarity":  "+", "modality": "statement", "evidence_indices": []any{0},
		}},
	})
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, "failed", structured.Result["processing_state"])
	require.Equal(t, "terminal-failure", structured.Result["correlation_id"])
	require.NoError(t, ValidateInput(Tool{InputSchema: dreamTerminalRememberOutputSchema(domain.ContractVersion)}, structured.Result))
	errors, ok := structured.Result["errors"].([]any)
	require.True(t, ok)
	require.Len(t, errors, 1)
}

func TestResolveDreamFeedbackReturnsStructuredNonCompletedTerminalOutcomes(t *testing.T) {
	for _, test := range []struct {
		state string
		code  rememberapp.TerminalErrorCode
	}{
		{state: string(rememberapp.TerminalProcessingRejected), code: rememberapp.TerminalErrorNoSupportedMemory},
		{state: string(rememberapp.TerminalProcessingQuarantined), code: rememberapp.TerminalErrorQuarantined},
	} {
		t.Run(test.state, func(t *testing.T) {
			terminal := &rememberapp.TerminalRememberResult{
				ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
				ProcessingState: test.state, SearchState: string(rememberapp.TerminalSearchNotRequired),
				CorrelationID: "terminal-" + test.state, Evidence: []rememberapp.TerminalEvidenceResult{},
				RelationshipResults: []rememberapp.SubmissionRelationshipResult{},
				Errors:              []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusError(test.code)},
			}
			reg, err := BuildActive(Dependencies{Dreams: &stubDreamService{resolveResult: &dreamservice.ResolveFeedbackResult{
				Dream: stubDream("profile-dream"), Memory: &rememberapp.RememberResult{Terminal: terminal},
			}}})
			require.NoError(t, err)
			tool, ok := reg.Get(ToolResolveDreamFeedback)
			require.True(t, ok)
			_, err = tool.Invoke(contractInvokeContext("write"), "profile-dream", map[string]any{
				"hypothesis_id": "dream-1", "decision": "confirm_true",
				"evidence": []any{map[string]any{"content": "independent evidence"}},
				"relationships": []any{map[string]any{
					"ref": "r-1", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
					"predicate": map[string]any{"proposed_key": "uses"},
					"object":    map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
					"polarity":  "+", "modality": "statement", "evidence_indices": []any{0},
				}},
			})
			structured, ok := ToolResultFromError(err)
			require.True(t, ok)
			require.Equal(t, test.state, structured.Result["processing_state"])
			require.NoError(t, ValidateInput(Tool{InputSchema: dreamTerminalRememberOutputSchema(domain.ContractVersion)}, structured.Result))
		})
	}
}

func TestResolveDreamFeedbackOutputSchemaCoversTerminalBranch(t *testing.T) {
	variants, ok := resolveDreamFeedbackOutputSchema()["oneOf"].([]any)
	require.True(t, ok)
	require.Len(t, variants, 3)
	terminal, ok := variants[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{
		"contract_version", "submission_id", "submission_kind", "processing_state", "search_state",
		"correlation_id", "evidence", "relationship_results", "errors",
	}, terminal["required"])
	terminalErrors := terminal["properties"].(map[string]any)["errors"].(map[string]any)["items"].(map[string]any)
	terminalActions := terminalErrors["properties"].(map[string]any)["next_action"].(map[string]any)["enum"].([]string)
	require.Contains(t, terminalActions, string(rememberapp.TerminalNextActionRetryDreamFeedback))
	genericErrors := terminalRememberOutputSchema(domain.ContractVersion)["properties"].(map[string]any)["errors"].(map[string]any)["items"].(map[string]any)
	genericActions := genericErrors["properties"].(map[string]any)["next_action"].(map[string]any)["enum"].([]string)
	require.NotContains(t, genericActions, string(rememberapp.TerminalNextActionRetryDreamFeedback))
}

func TestResolveDreamFeedbackReturnsStructuredBusyRetry(t *testing.T) {
	reg, err := BuildActive(Dependencies{Dreams: &stubDreamService{
		resolveErr: &dreamservice.ConfirmationBusyError{},
	}})
	require.NoError(t, err)
	tool, ok := reg.Get(ToolResolveDreamFeedback)
	require.True(t, ok)

	_, err = tool.Invoke(contractInvokeContext("write"), "profile-dream", map[string]any{
		"hypothesis_id": "dream-1", "decision": "confirm_true",
		"evidence": []any{map[string]any{"content": "independent evidence"}},
		"relationships": []any{map[string]any{
			"ref": "r-1", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
			"polarity":  "+", "modality": "statement", "evidence_indices": []any{0},
		}},
	})
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, "dream_confirmation_busy", structured.Result["code"])
	require.Equal(t, string(rememberapp.TerminalNextActionRetryDreamFeedback), structured.Result["next_action"])
	require.True(t, structured.Result["retryable"].(bool))
	require.NoError(t, ValidateInput(Tool{InputSchema: dreamConfirmationBusyOutputSchema()}, structured.Result))
}

func TestResolveDreamFeedbackReturnsLifecycleCompatibleBusyRetry(t *testing.T) {
	reg, err := BuildActive(Dependencies{Dreams: &stubDreamService{
		resolveErr: &dreamservice.ConfirmationBusyError{Decision: "reject"},
	}})
	require.NoError(t, err)
	tool, ok := reg.Get(ToolResolveDreamFeedback)
	require.True(t, ok)

	_, err = tool.Invoke(contractInvokeContext("write"), "profile-dream", map[string]any{
		"hypothesis_id": "dream-1", "decision": "reject", "reason": "no longer useful",
	})
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, "dream_confirmation_busy", structured.Result["code"])
	require.Equal(t, "dream lifecycle feedback is already in progress", structured.Result["message"])
	require.Equal(t, string(rememberapp.TerminalNextActionRetryDreamFeedback), structured.Result["next_action"])
	require.NotContains(t, structured.Result["remediation"], "same evidence")
	require.Contains(t, structured.Result["remediation"], "omit idempotency_key")
	require.Contains(t, structured.Result["remediation"], "same decision and reason")
	require.NoError(t, ValidateInput(Tool{InputSchema: dreamConfirmationBusyOutputSchema()}, structured.Result))
}

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
	if resolveOut["hypothesis_id"] != "dream-1" || dreams.lastResolveReq.Decision != "reinforce" || dreams.lastResolveReq.Feedback != "prior conversation still makes this useful" || dreams.lastResolveReq.IdempotencyKey != "" {
		t.Fatalf("resolve_dream_feedback output = %v request = %+v", resolveOut, dreams.lastResolveReq)
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
