package repository

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRememberAttemptHashMatchesCurrentHash(t *testing.T) {
	if !domain.RememberRequestHashMatches("current", "any", "current", "") {
		t.Fatal("current request hash must match")
	}
	if domain.RememberRequestHashMatches("stored", "any", "current", "") {
		t.Fatal("different current request hash must not match")
	}
}

func TestRememberAttemptPhasePreservesExplicitPreflightPhase(t *testing.T) {
	require.Equal(t, "preflight", rememberAttemptPhase(RememberAttemptRecordInput{Outcome: "quarantined", FailedPhase: "preflight"}))
	require.Equal(t, "assessment", rememberAttemptPhase(RememberAttemptRecordInput{Outcome: "failed", FailedPhase: "assessment"}))
	require.Equal(t, "commit", rememberAttemptPhase(RememberAttemptRecordInput{Outcome: "completed"}))
	require.Equal(t, "preflight_quarantined", rememberAttemptEventKind(RememberAttemptRecordInput{Outcome: "quarantined", FailedPhase: "preflight"}))
	require.Equal(t, "assessment_failed", rememberAttemptEventKind(RememberAttemptRecordInput{Outcome: "failed", FailedPhase: "assessment"}))
	require.Equal(t, "commit_completed", rememberAttemptEventKind(RememberAttemptRecordInput{Outcome: "completed"}))
}

func TestRememberAttemptHashMatchesOnlyRecognizedMigrationContract(t *testing.T) {
	if !domain.RememberRequestHashMatches("migrated", domain.MigratedRememberRequestHashVersion, "current", "migrated") {
		t.Fatal("recognized migration hash must match")
	}
	if domain.RememberRequestHashMatches("migrated", "unrecognized", "current", "migrated") {
		t.Fatal("unrecognized migration contract must not match")
	}
}

func TestRememberFailureArtifactPurgeBatchIsBounded(t *testing.T) {
	require.Equal(t, rememberFailureArtifactPurgeBatchSize, 100)
}
