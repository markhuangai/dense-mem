package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberAttemptPhasePreservesExplicitPreflightPhase(t *testing.T) {
	require.Equal(t, "preflight", rememberAttemptPhase(RememberAttemptRecordInput{Outcome: "quarantined", FailedPhase: "preflight"}))
	require.Equal(t, "assessment", rememberAttemptPhase(RememberAttemptRecordInput{Outcome: "failed", FailedPhase: "assessment"}))
	require.Equal(t, "commit", rememberAttemptPhase(RememberAttemptRecordInput{Outcome: "completed"}))
	require.Equal(t, "preflight_quarantined", rememberAttemptEventKind(RememberAttemptRecordInput{Outcome: "quarantined", FailedPhase: "preflight"}))
	require.Equal(t, "assessment_failed", rememberAttemptEventKind(RememberAttemptRecordInput{Outcome: "failed", FailedPhase: "assessment"}))
	require.Equal(t, "commit_completed", rememberAttemptEventKind(RememberAttemptRecordInput{Outcome: "completed"}))
}

func TestRememberFailureArtifactPurgeBatchIsBounded(t *testing.T) {
	require.Equal(t, rememberFailureArtifactPurgeBatchSize, 100)
}

func TestRememberAttemptRetryabilityDefaultsAndValidation(t *testing.T) {
	legacyFailure := normalizeRememberAttemptRecord(RememberAttemptRecordInput{Outcome: "failed"})
	require.True(t, legacyFailure.Retryable)

	explicitNonRetryable := normalizeRememberAttemptRecord(RememberAttemptRecordInput{Outcome: "failed", RetryabilitySet: true})
	require.False(t, explicitNonRetryable.Retryable)

	require.ErrorContains(t, validateRememberAttemptRecord(RememberAttemptRecordInput{
		TeamID: uuidForAttemptTest(), OwnerProfileID: uuidForAttemptTest(), AttemptID: uuidForAttemptTest(),
		IdempotencyKey: "completed", RequestHash: "hash", ContractVersion: "contract", SubmissionKind: "remember",
		Outcome: "completed", Retryable: true,
	}), "retryable is only valid for failed outcomes")
}

func uuidForAttemptTest() string {
	return "00000000-0000-4000-8000-000000000999"
}
