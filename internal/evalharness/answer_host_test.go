package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

type answerTransportFunc func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f answerTransportFunc) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func answerActionJSON(t *testing.T, action evidenceOnlyHostAction) string {
	t.Helper()
	raw, err := json.Marshal(action)
	require.NoError(t, err)
	return string(raw)
}

func TestEvidenceOnlyUnitAnswerHostPoliciesAndRegeneration(t *testing.T) {
	for _, policy := range []string{"one_call", "iterative"} {
		t.Run(policy, func(t *testing.T) {
			var tools []string
			server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tools = append(tools, r.URL.Path)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"evidence_id": "one", "context": "Alpha is the first Greek letter."}}}))
			}))
			defer server.Close()
			turn := 0
			transport := answerTransportFunc(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				turn++
				require.Equal(t, "dense_mem_evidence_only_host_v5", request.SchemaName)
				require.Equal(t, 8192, request.MaxOutputTokens)
				require.NotContains(t, request.Messages[1].Content, "reference_answer")
				action := evidenceOnlyHostAction{Action: "answer", Answer: "Alpha is the first Greek letter.", Citations: []EvidenceOnlyHostCitation{{EvidenceID: "one", Quote: "first Greek letter"}}}
				if turn == 1 {
					return modelprovider.StructuredResult{Content: `{"action":"answer"}`}, nil
				}
				if turn == 2 {
					require.Len(t, request.Messages, 3)
					require.Contains(t, request.Messages[2].Content, "complete replacement")
				}
				if policy == "iterative" {
					switch turn {
					case 2:
						action = evidenceOnlyHostAction{Action: "trace", EvidenceID: "one", Citations: []EvidenceOnlyHostCitation{}}
					case 3:
						action = evidenceOnlyHostAction{Action: "recall", Query: "alpha details", Citations: []EvidenceOnlyHostCitation{}}
					}
				} else {
					require.Equal(t, []string{"answer"}, request.Schema["properties"].(map[string]any)["action"].(map[string]any)["enum"])
				}
				return modelprovider.StructuredResult{Content: answerActionJSON(t, action), PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130}, nil
			})
			result, err := runEvidenceOnlyHostCase(context.Background(), &HTTPClient{BaseURL: server.URL, APIKey: "fixture-key"}, transport, "dense-mem-testing", policy, Case{CaseID: "case", Query: "What is alpha?"}, assessor.DefaultSemanticAssessmentLimits())
			require.NoError(t, err)
			require.Equal(t, "completed", result.Outcome)
			require.Contains(t, result.Answer, "first Greek letter")
			require.Equal(t, turn, result.ProviderCalls)
			require.Equal(t, "unavailable", result.Usage[0]["state"])
			require.Equal(t, "observed", result.Usage[1]["state"])
			require.Positive(t, result.ElapsedMS)
			if policy == "iterative" {
				require.Equal(t, []string{"tool:recall_memory", "tool:trace_memory", "tool:recall_memory"}, tools)
			} else {
				require.Equal(t, []string{"tool:recall_memory"}, tools)
			}
		})
	}
}

func TestEvidenceOnlyUnitAnswerHostStopsAtEightMemoryCalls(t *testing.T) {
	server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"results": []any{}}))
	}))
	defer server.Close()
	turns := 0
	transport := answerTransportFunc(func(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		turns++
		action := evidenceOnlyHostAction{Action: "recall", Query: "more", Citations: []EvidenceOnlyHostCitation{}}
		if turns == 8 {
			require.Equal(t, []string{"answer"}, r.Schema["properties"].(map[string]any)["action"].(map[string]any)["enum"])
			action.Action, action.Answer = "answer", "Insufficient evidence to answer."
		}
		return modelprovider.StructuredResult{Content: answerActionJSON(t, action)}, nil
	})
	result, err := runEvidenceOnlyHostCase(context.Background(), &HTTPClient{BaseURL: server.URL, APIKey: "fixture-key"}, transport, "dense-mem-testing", "iterative", Case{Query: "question"}, assessor.DefaultSemanticAssessmentLimits())
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 8)
	require.Equal(t, 8, result.ProviderCalls)
}

func TestEvidenceOnlyUnitAnswerHostFailuresRemainVisible(t *testing.T) {
	for _, test := range []struct {
		name, output, want string
		limits             func(*assessor.SemanticAssessmentLimits)
		providerErr        error
		memoryStatus       int
		wantCalls          int
	}{
		{name: "invalid response", output: `{}`, want: "regeneration exhausted", wantCalls: 3},
		{name: "input budget", want: "request exceeds context budget", limits: func(l *assessor.SemanticAssessmentLimits) { l.MaxInputTokens = 1 }},
		{name: "tokenizer", want: "request exceeds context budget", limits: func(l *assessor.SemanticAssessmentLimits) { l.Tokenizer = "unknown-tokenizer" }},
		{name: "output budget", output: `{"action":"answer","query":"","evidence_id":"","answer":"Abstain.","citations":[]}`, want: "response exceeds context budget", limits: func(l *assessor.SemanticAssessmentLimits) { l.MaxOutputTokens = 1 }, wantCalls: 3},
		{name: "provider cancellation", providerErr: context.Canceled, want: "context canceled", wantCalls: 1},
		{name: "memory failure", memoryStatus: http.StatusServiceUnavailable, want: "503"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.memoryStatus != 0 {
					w.WriteHeader(test.memoryStatus)
					return
				}
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			defer server.Close()
			limits := assessor.DefaultSemanticAssessmentLimits()
			if test.limits != nil {
				test.limits(&limits)
			}
			transport := answerTransportFunc(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				return modelprovider.StructuredResult{Content: test.output}, test.providerErr
			})
			result, err := runEvidenceOnlyHostCase(context.Background(), &HTTPClient{BaseURL: server.URL, APIKey: "fixture-key"}, transport, "dense-mem-testing", "one_call", Case{Query: "question"}, limits)
			require.ErrorContains(t, err, test.want)
			require.Equal(t, test.wantCalls, result.ProviderCalls)
			require.Len(t, result.ToolCalls, 1)
		})
	}
}

func TestEvidenceOnlyUnitAnswerHostPersistsOnlySelectedCasesAndProtectsFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			root := writeEvalFixture(t)
			seed := filepath.Join(root, "seed_manifest.json")
			var manifest SeedManifest
			require.NoError(t, readJSONFile(seed, &manifest))
			manifest.AnswersFile = "answers.jsonl"
			require.NoError(t, writeJSONFile(seed, manifest))
			require.NoError(t, writeJSONL(filepath.Join(root, manifest.AnswersFile), []AnswerLabel{{CaseID: "case-1", ReferenceAnswer: "private reference sentinel"}, {CaseID: "case-2"}}))
			server := newEvalHarnessServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"results":[]}`)) }))
			defer server.Close()
			const secret = "host-operational-credential-canary"
			transport := answerTransportFunc(func(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				for _, m := range r.Messages {
					require.NotContains(t, m.Content, "private reference sentinel")
				}
				if fail {
					return modelprovider.StructuredResult{}, errors.New("provider failed: " + secret)
				}
				return modelprovider.StructuredResult{Content: `{"action":"answer","query":"","evidence_id":"","answer":"Insufficient evidence.","citations":[]}`}, nil
			})
			opts := RunOptions{SeedManifestPath: seed, SuitePath: filepath.Join(root, "suite.jsonl"), BaseURL: server.URL, APIKey: "fixture-key", OutDir: filepath.Join(root, "host"), RunID: "host-fixture", Protector: observability.NewCredentialProtector(secret), Logger: observability.New(slog.LevelError)}
			err := RunEvidenceOnlyHost(context.Background(), opts, transport, "dense-mem-testing", "one_call", assessor.DefaultSemanticAssessmentLimits())
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var rows []json.RawMessage
			require.NoError(t, readJSONL(filepath.Join(opts.OutDir, "host_results.jsonl"), &rows))
			require.Len(t, rows, 2)
			var terminal EvidenceOnlyHostResult
			require.NoError(t, json.Unmarshal(rows[1], &terminal))
			require.Equal(t, "case-1", terminal.CaseID)
			require.NotContains(t, string(rows[1]), secret)
			if fail {
				require.Equal(t, "failed", terminal.Outcome)
				require.Contains(t, terminal.Error, "provider failed")
			} else {
				require.Equal(t, "completed", terminal.Outcome)
			}
			raw, err := os.ReadFile(filepath.Join(opts.OutDir, "host_results.jsonl"))
			require.NoError(t, err)
			require.ErrorIs(t, RunEvidenceOnlyHost(context.Background(), opts, transport, "dense-mem-testing", "one_call", assessor.DefaultSemanticAssessmentLimits()), os.ErrExist)
			after, err := os.ReadFile(filepath.Join(opts.OutDir, "host_results.jsonl"))
			require.NoError(t, err)
			require.Equal(t, raw, after)
			opts.OutDir = filepath.Join(root, "cancelled")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.ErrorIs(t, RunEvidenceOnlyHost(ctx, opts, transport, "dense-mem-testing", "one_call", assessor.DefaultSemanticAssessmentLimits()), context.Canceled)
		})
	}
}

func TestEvidenceOnlyUnitAnswerHostRejectsInvalidActionsAndConfiguration(t *testing.T) {
	for _, action := range []evidenceOnlyHostAction{
		{Action: "remember", Citations: []EvidenceOnlyHostCitation{}},
		{Action: "answer", Answer: " ", Citations: []EvidenceOnlyHostCitation{}},
		{Action: "recall", Query: " ", Citations: []EvidenceOnlyHostCitation{}},
		{Action: "recall", Query: strings.Repeat("界", 513), Citations: []EvidenceOnlyHostCitation{}},
	} {
		require.Error(t, validateEvidenceOnlyHostAction(action, nil, []string{"answer", "recall", "trace"}))
	}
	transport := answerTransportFunc(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		t.Fatal("unexpected provider call")
		return modelprovider.StructuredResult{}, nil
	})
	for _, test := range []struct {
		policy, model, out string
		transport          modelprovider.StructuredTransport
	}{
		{"invalid", "dense-mem-testing", "out", transport}, {"one_call", "other", "out", transport}, {"one_call", "dense-mem-testing", "", transport}, {"one_call", "dense-mem-testing", "out", nil},
	} {
		require.ErrorContains(t, RunEvidenceOnlyHost(context.Background(), RunOptions{OutDir: test.out}, test.transport, test.model, test.policy, assessor.DefaultSemanticAssessmentLimits()), "intake host requires")
	}
}
