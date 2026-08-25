package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestClaimPlacementRunEnforcesOwnerLeaseAttemptsAndSingleWinner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-claim-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-claim-owner")
	foreignOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-claim-foreign-owner")
	ledger := NewLedgerRepository(appDB, rls)

	createRun := func(key string) *CreateIngestResult {
		t.Helper()
		result, err := ledger.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: key + "-hash",
			Evidence: []EvidenceInput{{Content: "Claim exact Remember work."}},
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		return result
	}

	first := createRun("remember-claim-owner")
	claimed, err := ledger.ClaimPlacementRun(ctx, ClaimPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: first.IngestID, WorkerID: "claim-owner", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "processing", claimed.Status)

	foreignClaim, err := ledger.ClaimPlacementRun(ctx, ClaimPlacementRunInput{
		TeamID: teamID, OwnerProfileID: foreignOwnerID, IngestID: first.IngestID, WorkerID: "claim-foreign", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Nil(t, foreignClaim)

	unexpiredClaim, err := ledger.ClaimPlacementRun(ctx, ClaimPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: first.IngestID, WorkerID: "claim-second", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Nil(t, unexpiredClaim)

	exhausted := createRun("remember-claim-exhausted")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE placement_runs
			SET attempts = max_attempts, status = 'queued', available_at = now(), updated_at = now()
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, exhausted.IngestID).Error
	}))
	exhaustedClaim, err := ledger.ClaimPlacementRun(ctx, ClaimPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: exhausted.IngestID, WorkerID: "claim-exhausted", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Nil(t, exhaustedClaim)

	race := createRun("remember-claim-race")
	start := make(chan struct{})
	results := make(chan *PlacementRun, 2)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			result, claimErr := ledger.ClaimPlacementRun(ctx, ClaimPlacementRunInput{
				TeamID: teamID, OwnerProfileID: ownerID, IngestID: race.IngestID,
				WorkerID: "claim-race-" + string(rune('a'+index)), Lease: time.Minute,
			})
			results <- result
			errors <- claimErr
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)

	winners := 0
	for result := range results {
		if result != nil {
			winners++
			require.Equal(t, "processing", result.Status)
		}
	}
	for claimErr := range errors {
		require.NoError(t, claimErr)
	}
	require.Equal(t, 1, winners)
}

func TestTerminalizedRememberFailureAllowsSameKeyRetry(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-failed-retry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-failed-retry-owner")
	ledger := NewLedgerRepository(appDB, rls)

	input := CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "remember-failed-retry",
		RequestHash: "remember-failed-retry-hash", Evidence: []EvidenceInput{{Content: "Retryable Remember failure."}},
	}
	first, err := ledger.CreateIngest(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, first)

	require.NoError(t, ledger.TerminalizeRememberFailure(ctx, RememberTerminalizeFailureInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: first.IngestID,
		FailedPhase: "embedding", ErrorCode: "embedding_unavailable",
	}))

	retry, err := ledger.CreateIngest(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, retry)
	require.False(t, retry.Existing)
	require.NotEqual(t, first.IngestID, retry.IngestID)
}
