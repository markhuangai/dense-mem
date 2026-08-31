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
