package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestV2PlacementReviewRejectsStaleMigrationClaimEpoch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "placement-stale-migration-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "stale-migration-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	controlRepo := NewV2MigrationControlRepository(appDB, rls)

	run, err := controlRepo.CreateRun(ctx, V2CreateMigrationRunInput{
		MigrationContractVersion: "migration-contract-v1",
		CorpusVersion:            "corpus-v1",
		SourceKind:               "neo4j",
		State:                    domain.V2MigrationStateReady,
		Phase:                    "preflight",
		Required:                 true,
		PreflightApproved:        true,
	})
	require.NoError(t, err)
	run, err = controlRepo.UpdateRunState(ctx, V2UpdateMigrationRunStateInput{
		RunID:     run.RunID,
		FromState: domain.V2MigrationStateReady,
		ToState:   domain.V2MigrationStateRunning,
		Phase:     "migration",
		Retryable: true,
	})
	require.NoError(t, err)

	ingest, err := ledgerRepo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "placement-stale-migration-claim",
		RequestHash:    sha256Hex("Migration evidence should be fenced."),
		MigrationRunID: run.RunID,
		Evidence: []V2EvidenceInput{{
			Content: "Migration evidence should be fenced.",
		}},
	})
	require.NoError(t, err)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-stale-migration", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, run.RunID, claimed.MigrationRunID)
	require.Equal(t, 1, claimed.MigrationEpoch)

	_, err = controlRepo.UpdateRunState(ctx, V2UpdateMigrationRunStateInput{
		RunID:     run.RunID,
		FromState: domain.V2MigrationStateRunning,
		ToState:   domain.V2MigrationStatePaused,
		Phase:     "paused",
		Retryable: true,
	})
	require.NoError(t, err)

	_, err = ledgerRepo.RequeuePlacementReviewResult(ctx, V2RequeuePlacementReviewInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-stale-migration",
		ExpectedAttempts: claimed.Attempts,
		MigrationRunID:   claimed.MigrationRunID,
		MigrationEpoch:   claimed.MigrationEpoch,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2PlacementLeaseLost), err)
}
