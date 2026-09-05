package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdvisoryLockAdmissionsSharePoolBudgetAcrossEvidenceAndConfirmation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-shared-lock-budget-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-shared-lock-budget-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	result, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-shared-lock-budget-target",
		RequestHash: "evidence-dream-shared-lock-budget-target-hash", Evidence: []EvidenceInput{{
			Content:      "A shared pool budget protects nested callbacks.",
			InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, result)

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)

	entered := make(chan struct{})
	release := make(chan struct{})
	evidenceErr := make(chan error, 1)
	go func() {
		evidenceErr <- semantic.WithEvidenceDiscoveryTargetLock(ctx, teamID, fragment.FragmentID, fragment.ContentHash, func(EvidenceDiscoveryAttempt) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("evidence target lock callback did not start")
	}

	confirmationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	confirmationErr := make(chan error, 1)
	go func() {
		confirmationErr <- semantic.WithHypothesisConfirmationLock(confirmationCtx, teamID, uuid.NewString(), func(DreamRepository) error {
			return nil
		})
	}()
	select {
	case err := <-confirmationErr:
		require.ErrorIs(t, err, ErrDreamConfirmationBusy)
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("confirmation lock admission did not honor the shared pool budget")
	}

	close(release)
	require.NoError(t, <-evidenceErr)
}
