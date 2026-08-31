package repository

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberTerminalPublicResultUsesCanonicalErrorProjection(t *testing.T) {
	input := SynchronousRememberCommitInput{
		IngestID: uuid.NewString(),
		Metadata: map[string]any{"correlation_id": "terminal-correlation"},
		Commit: CommitSubmissionAssessmentInput{
			RelationshipResults: []SubmissionRelationshipResultInput{{RelationshipRef: "relationship-1"}},
		},
	}
	evidence := []EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0}}
	canonical := remember.TerminalStatusError(remember.TerminalErrorQuarantined)

	result := rememberTerminalPublicResult(input, evidence, "quarantined", RememberTerminalErrorInput{
		Code: canonical.Code, Message: canonical.Message, Retryable: canonical.Retryable,
		NextAction: canonical.NextAction, Remediation: canonical.Remediation,
	})
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var terminal remember.TerminalRememberResult
	require.NoError(t, json.Unmarshal(raw, &terminal))
	terminal.Kind = remember.ResultKindTerminal
	require.NoError(t, remember.ValidateTerminalRememberResult(&terminal, 1, []string{"relationship-1"}))
	require.Equal(t, canonical.Message, terminal.Errors[0].Message)
	require.Equal(t, canonical.Remediation, terminal.Errors[0].Remediation)
}
