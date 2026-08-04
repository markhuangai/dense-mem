package repository

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestPlacementReviewSecurityQuarantineRollsBackWithTerminalFailureAndRetriesOnce(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-security-quarantine-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-security-quarantine-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement security quarantine", "Unsafe placement evidence must quarantine atomically.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-security-quarantine", time.Minute)
	require.NoError(t, err)

	input := CompletePlacementReviewInput{
		TeamID:             teamID,
		OwnerProfileID:     ownerID,
		IngestID:           ingest.IngestID,
		PlacementRunID:     ingest.PlacementRunID,
		PlacementItemID:    ingest.Items[0].PlacementItemID,
		WorkerID:           "worker-security-quarantine",
		ExpectedAttempts:   claimed.Attempts,
		Status:             string(domain.SemanticReviewQuarantined),
		Category:           "quarantined",
		Payload:            map[string]any{"forced_terminal_failure": math.NaN()},
		SecurityQuarantine: deterministicPlacementSecurityQuarantine(ingest.Evidence[0].FragmentID),
	}
	_, err = ledgerRepo.CompletePlacementReviewResult(ctx, input)
	require.Error(t, err)

	var eventCount, quarantineCount, outcomeCount int64
	var runStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM evidence_security_events WHERE team_id = ?::uuid`, teamID).Scan(&eventCount).Error)
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM evidence_quarantines WHERE team_id = ?::uuid`, teamID).Scan(&quarantineCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND outcome_kind <> 'telemetry_first_disposition'
		`, teamID).Scan(&outcomeCount).Error)
		return tx.Raw(`SELECT status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error
	})
	require.NoError(t, err)
	assert.Zero(t, eventCount)
	assert.Zero(t, quarantineCount)
	assert.Zero(t, outcomeCount)
	assert.Equal(t, "processing", runStatus)

	input.Payload = map[string]any{"failure_stage": "deterministic_security_scan"}
	completed, err := ledgerRepo.CompletePlacementReviewResult(ctx, input)
	require.NoError(t, err)
	require.Equal(t, string(domain.SemanticReviewQuarantined), completed.Status)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM evidence_security_events WHERE team_id = ?::uuid`, teamID).Scan(&eventCount).Error)
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM evidence_quarantines WHERE team_id = ?::uuid`, teamID).Scan(&quarantineCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND outcome_kind <> 'telemetry_first_disposition'
		`, teamID).Scan(&outcomeCount).Error)
		return tx.Raw(`SELECT status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, ingest.PlacementRunID).Scan(&runStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), eventCount)
	assert.Equal(t, int64(1), quarantineCount)
	assert.Equal(t, int64(1), outcomeCount)
	assert.Equal(t, string(domain.PlacementRunQuarantined), runStatus)
}

func TestPlacementReviewSecurityQuarantineRequiresMatchingPlacementFragment(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-security-fragment-match-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-security-fragment-match-owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "placement-security-fragment-match",
		RequestHash:    "placement-security-fragment-match",
		Evidence: []EvidenceInput{
			{Content: "First placement item."},
			{Content: "Second placement item."},
		},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 2)
	require.Len(t, ingest.Items, 2)
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-security-fragment-match", time.Minute)
	require.NoError(t, err)

	_, err = ledgerRepo.CompletePlacementReviewResult(ctx, CompletePlacementReviewInput{
		TeamID:             teamID,
		OwnerProfileID:     ownerID,
		IngestID:           ingest.IngestID,
		PlacementRunID:     ingest.PlacementRunID,
		PlacementItemID:    ingest.Items[0].PlacementItemID,
		WorkerID:           "worker-security-fragment-match",
		ExpectedAttempts:   claimed.Attempts,
		Status:             string(domain.SemanticReviewQuarantined),
		Category:           "quarantined",
		SecurityQuarantine: deterministicPlacementSecurityQuarantine(ingest.Evidence[1].FragmentID),
	})
	require.ErrorContains(t, err, "security quarantine fragment must match placement item")
	assertPlacementSecurityQuarantineUncommitted(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
}

func TestPlacementReviewSecurityQuarantineRejectsForeignProfileFragment(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-security-foreign-fragment-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-security-foreign-fragment-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "placement-security-foreign-fragment-other")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"placement security foreign fragment", "Owner placement evidence.")
	claimed, err := ledgerRepo.ClaimNextPlacementRun(ctx, teamID, "worker-security-foreign-fragment", time.Minute)
	require.NoError(t, err)
	require.Equal(t, ingest.PlacementRunID, claimed.PlacementRunID)
	foreignIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, otherOwnerID,
		"placement security foreign fragment other", "Other profile placement evidence.")

	_, err = ledgerRepo.CompletePlacementReviewResult(ctx, CompletePlacementReviewInput{
		TeamID:             teamID,
		OwnerProfileID:     ownerID,
		IngestID:           ingest.IngestID,
		PlacementRunID:     ingest.PlacementRunID,
		PlacementItemID:    ingest.Items[0].PlacementItemID,
		WorkerID:           "worker-security-foreign-fragment",
		ExpectedAttempts:   claimed.Attempts,
		Status:             string(domain.SemanticReviewQuarantined),
		Category:           "quarantined",
		SecurityQuarantine: deterministicPlacementSecurityQuarantine(foreignIngest.Evidence[0].FragmentID),
	})
	require.ErrorContains(t, err, "security quarantine fragment must match placement item")
	assertPlacementSecurityQuarantineUncommitted(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
}

func deterministicPlacementSecurityQuarantine(fragmentID string) *PlacementSecurityQuarantineInput {
	return &PlacementSecurityQuarantineInput{
		FragmentID: fragmentID,
		SecurityEventDraft: SecurityEventDraft{
			EventKind:      "deterministic_scan",
			Decision:       "quarantine",
			ScanPolicyHash: "sha256:security-transaction-test",
			Reason:         "deterministic intake scan rejected evidence",
			Signals: []SecuritySignalInput{{
				Kind:      "instruction_override",
				Severity:  "critical",
				SpanStart: 0,
				SpanEnd:   6,
				Metadata:  map[string]any{"rule_id": "instruction_override"},
			}},
		},
	}
}

func assertPlacementSecurityQuarantineUncommitted(
	t *testing.T,
	ctx context.Context,
	appDB *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID, ownerID, placementRunID string,
) {
	t.Helper()
	var eventCount, quarantineCount, outcomeCount int64
	var runStatus string
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM evidence_security_events WHERE team_id = ?::uuid`, teamID).Scan(&eventCount).Error)
		require.NoError(t, tx.Raw(`SELECT COUNT(*) FROM evidence_quarantines WHERE team_id = ?::uuid`, teamID).Scan(&quarantineCount).Error)
		require.NoError(t, tx.Raw(`
			SELECT COUNT(*)
			FROM placement_outcomes
			WHERE team_id = ?::uuid
			  AND outcome_kind <> 'telemetry_first_disposition'
		`, teamID).Scan(&outcomeCount).Error)
		return tx.Raw(`SELECT status FROM placement_runs WHERE team_id = ?::uuid AND placement_run_id = ?::uuid`, teamID, placementRunID).Scan(&runStatus).Error
	})
	require.NoError(t, err)
	assert.Zero(t, eventCount)
	assert.Zero(t, quarantineCount)
	assert.Zero(t, outcomeCount)
	assert.Equal(t, "processing", runStatus)
}
