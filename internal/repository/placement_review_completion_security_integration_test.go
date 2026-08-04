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
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         ingest.IngestID,
		PlacementRunID:   ingest.PlacementRunID,
		PlacementItemID:  ingest.Items[0].PlacementItemID,
		WorkerID:         "worker-security-quarantine",
		ExpectedAttempts: claimed.Attempts,
		Status:           string(domain.SemanticReviewQuarantined),
		Category:         "quarantined",
		Payload:          map[string]any{"forced_terminal_failure": math.NaN()},
		SecurityQuarantine: &PlacementSecurityQuarantineInput{
			FragmentID: ingest.Evidence[0].FragmentID,
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
		},
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
