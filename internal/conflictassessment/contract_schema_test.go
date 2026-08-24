package conflictassessment

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConflictAssessmentResponseDecodesValidPayloadAndNormalizesLimits(t *testing.T) {
	response, err := DecodeConflictAssessmentResponseJSON(
		[]byte(`{"decision":"abstain","position_id":null,"confidence":0,"rationale":"uncertain"}`),
		SemanticAssessmentLimits{},
	)
	require.NoError(t, err)
	require.Equal(t, ConflictAssessmentDecisionAbstain, response.Decision)
	require.Greater(t, response.OutputTokens, 0)

	_, errs := PrepareConflictAssessmentRequest(ConflictAssessmentRequest{
		RequestID: "request", CaseID: "case", Version: 1, Question: "question",
		Positions: []ConflictAssessmentPosition{{PositionID: "a", PositionKey: "a"}, {PositionID: "b", PositionKey: "b"}},
		Evidence:  []ConflictAssessmentEvidence{{EvidenceID: "e", PositionID: "a", SupportID: "s", SupporterRef: "p", Content: strings.Repeat("e", 1), AcceptedAt: time.Now()}},
	}, SemanticAssessmentLimits{})
	require.Empty(t, errs)
}
