//go:build integration

package postgres

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecordRememberFailurePreservesUnavailableCaptureStateAfterAttemptBudget(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-attempt-diagnostic-budget")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-attempt-diagnostic-budget-owner")
	repo := NewStore(appDB, rls, ConflictRuntimeConfig{})

	body := bytes.Repeat([]byte("x"), maxRememberDiagnosticBodyBytes)
	diagnostics := make([]RememberAttemptDiagnosticInput, 5)
	for index := 0; index < 4; index++ {
		diagnostics[index] = RememberAttemptDiagnosticInput{
			SequenceNo:   index + 1,
			Kind:         "provider_exchange",
			Component:    "assessor",
			ResponseBody: body,
			Outcome:      "captured",
			CaptureState: "captured",
		}
	}
	diagnostics[4] = RememberAttemptDiagnosticInput{
		SequenceNo:    5,
		Kind:          "provider_exchange",
		Component:     "assessor",
		Outcome:       "provider_not_called",
		CaptureState:  "unavailable",
		CaptureReason: "credential_protection_2",
	}

	attemptID := uuid.NewString()
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamID, OwnerProfileID: ownerID, AttemptID: attemptID,
			IdempotencyKey: "remember-attempt-diagnostic-budget", RequestHash: "remember-attempt-diagnostic-budget-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "embedding", ErrorCode: "embedding_unavailable", PublicResult: map[string]any{},
		},
		Diagnostics: diagnostics,
	}))

	detail, err := repo.GetRememberAttemptDiagnostic(ctx, teamID, attemptID)
	require.NoError(t, err)
	require.Len(t, detail.Diagnostics, 5)
	require.Equal(t, "unavailable", detail.Diagnostics[4].CaptureState)
	require.Equal(t, "provider_not_called", detail.Diagnostics[4].Outcome)
	require.Empty(t, detail.Diagnostics[4].ResponseBody)
}
