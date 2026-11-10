package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
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
