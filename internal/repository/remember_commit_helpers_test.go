package repository

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateEvidenceSecurityResultsRequiresCompleteSafeSet(t *testing.T) {
	firstID, secondID := uuid.NewString(), uuid.NewString()
	evidence := []EvidenceInput{{FragmentID: firstID}, {FragmentID: secondID}}
	valid := []EvidenceSecurityResult{
		{FragmentID: firstID, EvidenceIndex: 0, Decision: "pass", Safe: true},
		{FragmentID: secondID, EvidenceIndex: 1, Decision: "pass", Safe: true},
	}
	require.NoError(t, validateEvidenceSecurityResults(evidence, valid, false))

	tests := []struct {
		name   string
		mutate func([]EvidenceSecurityResult) []EvidenceSecurityResult
		want   string
	}{
		{name: "missing result", mutate: func(results []EvidenceSecurityResult) []EvidenceSecurityResult { return results[:1] }, want: "exactly one result per evidence item"},
		{name: "duplicate result", mutate: func(results []EvidenceSecurityResult) []EvidenceSecurityResult {
			return []EvidenceSecurityResult{results[0], results[0]}
		}, want: "duplicated"},
		{name: "unknown evidence", mutate: func(results []EvidenceSecurityResult) []EvidenceSecurityResult {
			results[0].FragmentID = uuid.NewString()
			return results
		}, want: "outside the Remember request"},
		{name: "unsafe pass", mutate: func(results []EvidenceSecurityResult) []EvidenceSecurityResult {
			results[0].Safe = false
			return results
		}, want: "pass must be marked safe"},
		{name: "unsafe signal", mutate: func(results []EvidenceSecurityResult) []EvidenceSecurityResult {
			results[0].Signals = []SecuritySignalInput{{Kind: "signal"}}
			return results
		}, want: "contains unsafe signals"},
		{name: "missing decision", mutate: func(results []EvidenceSecurityResult) []EvidenceSecurityResult {
			results[0].Decision = ""
			return results
		}, want: "has unsupported decision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results := make([]EvidenceSecurityResult, len(valid))
			copy(results, valid)
			require.ErrorContains(t, validateEvidenceSecurityResults(evidence, test.mutate(results), false), test.want)
		})
	}

	quarantine := []EvidenceSecurityResult{
		{FragmentID: firstID, EvidenceIndex: 0, Decision: "quarantine", Safe: false},
		{FragmentID: secondID, EvidenceIndex: 1, Decision: "pass", Safe: true},
	}
	require.ErrorContains(t, validateEvidenceSecurityResults(evidence, quarantine, false), "is not safe")
	require.NoError(t, validateEvidenceSecurityResults(evidence, quarantine, true))
}

func TestValidateSynchronousRememberCommitInputAllowsZeroRelationshipsOnlyWithSecurityResults(t *testing.T) {
	teamID, ownerID, ingestID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fragmentID := uuid.NewString()
	assessmentID := uuid.NewString()
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
		IdempotencyKey: "evidence-only", RequestHash: "sha256:evidence-only", AssessmentID: assessmentID,
		Evidence: []EvidenceInput{{FragmentID: fragmentID}},
		EvidenceSecurityResults: []EvidenceSecurityResult{{
			FragmentID: fragmentID, EvidenceIndex: 0, Decision: "pass", Safe: true,
		}},
		Commit: CommitSubmissionAssessmentInput{
			RememberCommitScope: RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID},
			AssessmentID:        assessmentID,
			Items:               []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
		},
	}
	require.NoError(t, validateSynchronousRememberCommitInput(normalizeSynchronousRememberCommitInput(input)))

	input.EvidenceSecurityResults = nil
	require.ErrorContains(t, validateSynchronousRememberCommitInput(normalizeSynchronousRememberCommitInput(input)), "exactly one result per evidence item")
}
