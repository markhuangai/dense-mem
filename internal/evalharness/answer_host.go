package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type EvidenceOnlyHostCitation struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote"`
}

type evidenceOnlyHostAction struct {
	Action     string                     `json:"action"`
	Query      string                     `json:"query"`
	EvidenceID string                     `json:"evidence_id"`
	Answer     string                     `json:"answer"`
	Citations  []EvidenceOnlyHostCitation `json:"citations"`
}

type evidenceOnlyHostCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
	Result    map[string]any `json:"result"`
	ElapsedMS float64        `json:"elapsed_ms"`
}

type EvidenceOnlyHostResult struct {
	CaseID        string                     `json:"case_id"`
	Policy        string                     `json:"policy"`
	Outcome       string                     `json:"outcome"`
	Answer        string                     `json:"answer"`
	Citations     []EvidenceOnlyHostCitation `json:"citations"`
	ToolCalls     []evidenceOnlyHostCall     `json:"tool_calls"`
	ProviderCalls int                        `json:"provider_calls"`
	Usage         []map[string]any           `json:"usage"`
	ElapsedMS     float64                    `json:"client_elapsed_ms"`
	Error         string                     `json:"error,omitempty"`
}

// RunEvidenceOnlyHost selects answerable cases offline; references never enter runEvidenceOnlyHostCase.
func RunEvidenceOnlyHost(ctx context.Context, opts RunOptions, transport modelprovider.StructuredTransport, model, policy string, limits assessor.SemanticAssessmentLimits) error {
	if (policy != "one_call" && policy != "iterative") || transport == nil || model != "dense-mem-testing" || opts.OutDir == "" {
		return errors.New("intake host requires a policy, private output, and dense-mem-testing transport")
	}
	manifest, _, cases, _, suite, _, err := loadRunInputs(opts.SeedManifestPath, opts.SuitePath)
	if err != nil {
		return err
	}
	var labels []AnswerLabel
	if err := readJSONL(resolveSeedPath(opts.SeedManifestPath, manifest.AnswersFile), &labels); err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, label := range labels {
		if strings.TrimSpace(label.ReferenceAnswer) != "" {
			selected[label.CaseID] = true
		}
	}
	planned := []string{}
	for _, item := range suite {
		if selected[item.CaseID] {
			planned = append(planned, item.CaseID)
		}
	}
	if manifest.SeedID == "public_6axis_1k_v3" && len(planned) != 82 {
		return errors.New("intake host reference-answer cohort changed")
	}
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(opts.OutDir, "host_results.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := writeJSONFile(filepath.Join(opts.OutDir, "host_plan.json"), map[string]any{"schema_version": "dense-mem.eval.evidence-only-host.v1", "case_ids": planned, "policy": policy, "model": model, "limits": limits, "run_id": opts.RunID, "answer_contract_sha256": EvidenceOnlyAnswerContractHash(), "started_at": time.Now().UTC()}); err != nil {
		return err
	}
	index := IndexCases(cases)
	encoder := json.NewEncoder(file)
	client := &HTTPClient{BaseURL: opts.BaseURL, APIKey: opts.APIKey}
	for _, id := range planned {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(map[string]any{"event": "started", "case_id": id, "policy": policy}); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		caseCtx := correlation.WithID(ctx, uuid.NewString())
		if opts.Logger != nil {
			opts.Logger.InfoContext(caseCtx, "intake_host_case_started", observability.String("run_id", opts.RunID), observability.String("case_id", id), observability.String("policy", policy))
		}
		result, runErr := runEvidenceOnlyHostCase(caseCtx, client, transport, model, policy, index[id], limits)
		if runErr != nil {
			protected := protectExecutionError(ctx, opts, runErr)
			result.Error = protected.Error()
			result.Outcome = "failed"
		}
		protectedResult := opts.Protector.Snapshot(result, 64<<20)
		if protectedResult.UnavailableReason != observability.CredentialProtectionAvailable {
			return errors.New("answer capture unavailable after credential protection")
		}
		if err := encoder.Encode(protectedResult.Value); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if opts.Logger != nil {
			opts.Logger.InfoContext(caseCtx, "intake_host_case_finished", observability.String("run_id", opts.RunID), observability.String("case_id", id), observability.String("outcome", result.Outcome), observability.Int("memory_tool_calls", len(result.ToolCalls)))
		}
		if runErr != nil {
			return runErr
		}
	}
	return nil
}

func runEvidenceOnlyHostCase(ctx context.Context, client *HTTPClient, transport modelprovider.StructuredTransport, model, policy string, item Case, limits assessor.SemanticAssessmentLimits) (result EvidenceOnlyHostResult, runErr error) {
	started := time.Now()
	result = EvidenceOnlyHostResult{CaseID: item.CaseID, Policy: policy, Outcome: "completed", Citations: []EvidenceOnlyHostCitation{}, ToolCalls: []evidenceOnlyHostCall{}, Usage: []map[string]any{}}
	defer func() { result.ElapsedMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	call := func(tool string, arguments map[string]any) error {
		started := time.Now()
		output := map[string]any{}
		err := client.CallTool(ctx, tool, arguments, &output)
		result.ToolCalls = append(result.ToolCalls, evidenceOnlyHostCall{Tool: tool, Arguments: arguments, Result: output, ElapsedMS: float64(time.Since(started)) / float64(time.Millisecond)})
		return err
	}
	if err := call("recall_memory", map[string]any{"query": item.Query, "limit": 10, "topic_limit": 3}); err != nil {
		return result, err
	}
	messages := []modelprovider.Message{{Role: "system", Content: evidenceOnlyHostInstruction}}
	for {
		body, err := json.Marshal(map[string]any{"question": item.Query, "policy": policy, "memory_calls": result.ToolCalls, "remaining_memory_calls": 8 - len(result.ToolCalls)})
		if err != nil {
			return result, err
		}
		base := append(append([]modelprovider.Message(nil), messages...), modelprovider.Message{Role: "user", Content: string(body)})
		actions := []string{"answer", "recall", "trace"}
		if policy == "one_call" || len(result.ToolCalls) == 8 {
			actions = []string{"answer"}
		}
		var action evidenceOnlyHostAction
		for turn := 1; turn <= 3; turn++ {
			schema := evidenceOnlyHostSchema(actions)
			converted := []assessor.SemanticAssessmentProviderMessage{}
			for _, message := range base {
				converted = append(converted, assessor.SemanticAssessmentProviderMessage{Role: message.Role, Content: message.Content})
			}
			count, err := assessor.CountSemanticAssessmentProviderRequestTokens(model, "dense_mem_evidence_only_host_v5", schema, limits.ProviderTemperatureDisabled, converted, limits.Tokenizer)
			if err != nil || count > limits.MaxInputTokens {
				return result, errors.New("intake host complete request exceeds context budget")
			}
			response, err := transport.Complete(ctx, modelprovider.StructuredRequest{Model: model, Messages: base, SchemaName: "dense_mem_evidence_only_host_v5", Schema: schema, MaxInputTokens: limits.MaxInputTokens, MaxOutputTokens: min(limits.MaxOutputTokens, 8192)})
			result.ProviderCalls++
			usage := map[string]any{"state": "unavailable"}
			if response.TotalTokens > 0 {
				usage = map[string]any{"state": "observed", "input_tokens": response.PromptTokens, "output_tokens": response.CompletionTokens, "total_tokens": response.TotalTokens}
			}
			result.Usage = append(result.Usage, usage)
			if err != nil {
				return result, err
			}
			action = evidenceOnlyHostAction{}
			err = jsonstrict.Decode(bytes.NewReader([]byte(response.Content)), &action, 1<<20)
			var fields map[string]json.RawMessage
			if err == nil {
				err = json.Unmarshal([]byte(response.Content), &fields)
			}
			for _, key := range []string{"action", "query", "evidence_id", "answer", "citations"} {
				if len(fields[key]) == 0 || string(fields[key]) == "null" {
					err = errors.New("host response requires every closed-schema field")
				}
			}
			if err == nil {
				err = validateEvidenceOnlyHostAction(action, result.ToolCalls, actions)
			}
			outputTokens, tokenErr := assessor.CountTokens(response.Content, limits.Tokenizer)
			if tokenErr != nil || outputTokens > min(limits.MaxOutputTokens, 8192) || response.PromptTokens > limits.MaxInputTokens || response.CompletionTokens > min(limits.MaxOutputTokens, 8192) {
				err = errors.New("intake host response exceeds context budget")
			}
			if err == nil {
				break
			}
			if turn == 3 {
				return result, fmt.Errorf("intake host complete-response regeneration exhausted: %w", err)
			}
			base = append(append([]modelprovider.Message(nil), base[:len(messages)+1]...), modelprovider.Message{Role: "user", Content: "Return one complete replacement object. Validation error: " + err.Error()})
		}
		if action.Action == "answer" {
			result.Answer, result.Citations = action.Answer, action.Citations
			return result, nil
		}
		if action.Action == "recall" {
			if err := call("recall_memory", map[string]any{"query": action.Query, "limit": 10, "topic_limit": 3}); err != nil {
				return result, err
			}
		} else if err := call("trace_memory", map[string]any{"evidence_id": action.EvidenceID}); err != nil {
			return result, err
		}
	}
}

func evidenceOnlyHostSchema(actions []string) map[string]any {
	text := map[string]any{"type": "string"}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action", "query", "evidence_id", "answer", "citations"}, "properties": map[string]any{
		"action": map[string]any{"type": "string", "enum": actions}, "query": map[string]any{"type": "string", "maxLength": 512}, "evidence_id": text, "answer": text,
		"citations": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"evidence_id", "quote"}, "properties": map[string]any{"evidence_id": text, "quote": text}}},
	}}
}

func validateEvidenceOnlyHostAction(action evidenceOnlyHostAction, calls []evidenceOnlyHostCall, actions []string) error {
	allowed := false
	for _, choice := range actions {
		allowed = allowed || action.Action == choice
	}
	if !allowed || action.Citations == nil {
		return errors.New("invalid host action or missing citations")
	}
	evidence := map[string][]string{}
	var collect func(any)
	collect = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			if id, ok := item["evidence_id"].(string); ok {
				for _, key := range []string{"context", "content", "quote"} {
					if text, ok := item[key].(string); ok {
						evidence[id] = append(evidence[id], text)
					}
				}
			}
			for _, child := range item {
				collect(child)
			}
		case []any:
			for _, child := range item {
				collect(child)
			}
		}
	}
	for _, call := range calls {
		collect(call.Result)
	}
	if action.Action == "trace" && len(evidence[action.EvidenceID]) == 0 {
		return errors.New("host trace is outside returned Evidence handles")
	}
	if action.Action == "recall" && (strings.TrimSpace(action.Query) == "" || len([]rune(action.Query)) > 512) {
		return errors.New("host recall query is missing or oversized")
	}
	if action.Action == "answer" && strings.TrimSpace(action.Answer) == "" {
		return errors.New("host answer or explicit abstention is required")
	}
	for _, citation := range action.Citations {
		if citation.Quote == "" || !exactAnswerQuote(evidence[citation.EvidenceID], citation.Quote) {
			return errors.New("host citation must quote returned evidence exactly")
		}
	}
	return nil
}
