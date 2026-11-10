package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvidenceOnlyUnitAnswerQuotationsCannotJoinSeparateEvidence(t *testing.T) {
	calls := []evidenceOnlyHostCall{{Tool: "recall_memory", Result: map[string]any{"results": []any{map[string]any{"evidence_id": "one", "context": "first fragment"}, map[string]any{"evidence_id": "one", "context": "second fragment"}}}}}
	action := evidenceOnlyHostAction{Action: "answer", Answer: "Supported answer", Citations: []EvidenceOnlyHostCitation{{EvidenceID: "one", Quote: "first fragment"}}}
	require.NoError(t, validateEvidenceOnlyHostAction(action, calls, []string{"answer"}))
	action.Citations[0].Quote = "fragment\nsecond"
	require.Error(t, validateEvidenceOnlyHostAction(action, calls, []string{"answer"}))
	action.Action = "trace"
	action.Citations = []EvidenceOnlyHostCitation{}
	action.EvidenceID = "foreign"
	require.Error(t, validateEvidenceOnlyHostAction(action, calls, []string{"trace"}))
	action.EvidenceID = "one"
	require.NoError(t, validateEvidenceOnlyHostAction(action, calls, []string{"trace"}))
}

func TestEvidenceOnlyUnitAnswerJudgmentsRequireCompleteCitationCoverage(t *testing.T) {
	raw := []byte(`{"answer_correct":true,"abstains":false,"claims":[{"claim":"A sourced claim","supported":true}],"citation_support":[{"citation_index":0,"supported":true,"reason":"Supports the claim"}],"reason":"Correct from the source"}`)
	require.NoError(t, validateAnswerJudgmentFields(raw))
	var judgment AnswerJudgment
	require.NoError(t, json.Unmarshal(raw, &judgment))
	require.NoError(t, validateAnswerJudgment(judgment, 1))
	require.Error(t, validateAnswerJudgment(judgment, 2))
	require.Error(t, validateAnswerJudgmentFields([]byte(strings.Replace(string(raw), `"supported":true`, `"other":true`, 1))))
	require.Error(t, validateAnswerJudgmentFields([]byte(strings.Replace(string(raw), `"answer_correct":true,`, "", 1))))
	judgment.Citations = append(judgment.Citations, judgment.Citations[0])
	require.Error(t, validateAnswerJudgment(judgment, 2))
}

func TestEvidenceOnlyUnitAnswerCapturesPreserveTypedReceiptsAndProtectCredentials(t *testing.T) {
	const secret = "answer-capture-operational-credential-canary"
	const admitted = "Exact source text with token and password field names."
	protector := observability.NewCredentialProtector(secret)
	started := time.Date(2026, time.November, 10, 13, 56, 47, 762892419, time.UTC)
	score := PairAnswerScore{SchemaVersion: "dense-mem.eval.answers.v5", StartedAt: started, Policies: map[string]AnswerPolicyScore{
		"one_call": {Policy: "one_call", Cases: 82, Correct: 77, CasesDetail: []AnswerScoredCase{{
			CaseID: "case-one", Judgment: AnswerJudgment{Reason: admitted},
			Attempts: []modelprovider.StructuredResult{{Content: secret, PromptTokens: 123, UsageAvailable: true}},
		}}},
	}}
	for name, value := range map[string]any{"score": score, "failure": map[string]any{"score": score, "error": "provider failed: " + secret}} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.json")
			require.NoError(t, writeProtectedAnswerJSON(path, value, protector))
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(raw), secret)
			require.Contains(t, string(raw), admitted)
			if name == "failure" {
				var failure map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &failure))
				raw = failure["score"]
			}
			var decoded PairAnswerScore
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.Equal(t, started, decoded.StartedAt)
			require.Equal(t, 82, decoded.Policies["one_call"].Cases)
			require.Equal(t, 123, decoded.Policies["one_call"].CasesDetail[0].Attempts[0].PromptTokens)
		})
	}
	host := EvidenceOnlyHostResult{CaseID: "case-one", Answer: admitted, Usage: []map[string]any{{"input_tokens": json.Number("9007199254740993"), "credential": secret}}}
	raw, err := protectAnswerJSON(host, protector)
	require.NoError(t, err)
	require.NotContains(t, string(raw), secret)
	require.Contains(t, string(raw), admitted)
	require.Contains(t, string(raw), `"input_tokens":9007199254740993`)
	var decoded EvidenceOnlyHostResult
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, host.Answer, decoded.Answer)
	path := filepath.Join(t.TempDir(), "unavailable.json")
	require.Error(t, writeProtectedAnswerJSON(path, map[string]any{"invalid": make(chan int)}, protector))
	require.NoFileExists(t, path)
}

func TestEvidenceOnlyUnitIntakeResubmissionKeepsEvidenceAndOriginalIdentity(t *testing.T) {
	input := corpusRememberInput(CorpusItem{SourceDocID: "source", Content: "Exact original Evidence."})
	id := uuid.NewString()
	failure := &StructuredToolError{Tool: "remember", Result: map[string]any{"submission_id": id, "processing_state": "failed", "errors": []any{map[string]any{"retryable": false, "next_action": "contact_operator"}}}}
	retried, original, retryable, ok := terminalRememberRetryInput("remember", input, failure)
	require.True(t, ok)
	require.False(t, retryable)
	require.Equal(t, id, original)
	require.Equal(t, "eval:source:resubmit1", retried["idempotency_key"])
	require.Equal(t, input["evidence"], retried["evidence"])
	require.Equal(t, "eval:source", input["idempotency_key"])
	_, _, _, ok = terminalRememberRetryInput("remember", retried, failure)
	require.False(t, ok)
	for _, cause := range []error{context.DeadlineExceeded, errors.New("ambiguous transport response"), &StructuredToolError{Tool: "remember", Result: map[string]any{"submission_id": id, "processing_state": "completed"}}} {
		_, _, _, ok = terminalRememberRetryInput("remember", input, cause)
		require.False(t, ok)
	}
}

func TestEvidenceOnlyUnitCampaignLaunchCannotRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch.json")
	require.NoError(t, StartEvidenceOnlyCampaign(path))
	var launch AnswerCampaignLaunch
	require.NoError(t, readPairJSON(path, &launch))
	require.Equal(t, 24*time.Hour, launch.Deadline.Sub(launch.StartedAt))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.ErrorIs(t, StartEvidenceOnlyCampaign(path), os.ErrExist)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NotEmpty(t, EvidenceOnlyAnswerContractHash())
}
