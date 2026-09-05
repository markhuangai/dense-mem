package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestEvidenceDiscoveryRecoveryExhaustionReportsPersistedTotals(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-recovery-exhaustion", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-recovery-exhaustion-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-recovery-exhaustion-owner")
	ledger := NewLedgerRepository(appDB, rls)
	search := NewSearchRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	result, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-recovery-exhaustion-target",
		RequestHash: "evidence-dream-recovery-exhaustion-target-hash", Evidence: []EvidenceInput{{
			Content: "Recovery exhaustion evidence.", InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	fragment := requireTestEvidenceFragment(t, result)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: fragment.FragmentID,
		SourceVersion: 1, DocumentText: fragment.Content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})

	scheduledFor := time.Now().UTC().Add(-time.Hour)
	run, err := semantic.ClaimScheduledDreamCycle(ctx, DreamCycleClaimInput{
		TeamID: teamID, RunDate: scheduledFor.Format("2006-01-02"),
		WindowKey: "hour:" + scheduledFor.Format("2006-01-02T15"), ScheduledFor: &scheduledFor,
		LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(-time.Minute), Lane: domain.DreamLaneEvidenceDiscovery,
	})
	require.NoError(t, err)
	targets, err := semantic.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceTargetTestLimit, evidenceContextTestLimit)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	target := targets[0].Target
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO dream_evidence_target_evaluations (
			    team_id, run_id, space_id, space_generation, target_evidence_id,
			    target_content_hash, pass_number, provider_model, provider_turns,
			    provider_input_tokens, provider_output_tokens, provider_proposals,
			    accepted_proposals, rejected_proposals, created_hypotheses
			) VALUES (?, ?::uuid, ?::uuid, ?, ?::uuid, ?, 1, 'recovery-exhaustion-test', 2, 3, 4, 5, 2, 1, 2)
		`, teamID, run.RunID, target.SpaceID, target.SpaceGeneration, target.EvidenceID, target.ContentHash).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE dream_cycle_runs
			SET attempt_count = 3, lease_until = now() - interval '1 minute'
			WHERE team_id = ?::uuid AND run_id = ?::uuid
		`, teamID, run.RunID).Error
	}))

	recovered, err := semantic.ClaimRecoverableScheduledDreamCycle(ctx, DreamCycleRecoveryClaimInput{
		TeamID: teamID, LeaseToken: uuid.NewString(), LeaseUntil: time.Now().UTC().Add(time.Minute),
		MaxAttempts: 3, Lane: domain.DreamLaneEvidenceDiscovery,
	})
	require.NoError(t, err)
	require.Nil(t, recovered, "an exhausted recovery must be finalized instead of reclaimed")
	runs, err := semantic.ListDreamCyclesForTeam(ctx, teamID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	failed := runs[0]
	require.Equal(t, "failed", failed.Status)
	require.Equal(t, 1, failed.EvidenceTargets)
	require.Equal(t, 1, failed.EvaluatedEvidenceTargets)
	require.Equal(t, 2, failed.CreatedHypotheses)
	require.Equal(t, 1, failed.RejectedHypotheses)
	require.Equal(t, 5, failed.ProviderProposals)
	require.Equal(t, 1, failed.OutcomeSummary["recovery_exhausted"])
	require.Equal(t, 1, failed.OutcomeSummary["evidence_targets"])
	require.Equal(t, 1, failed.OutcomeSummary["evaluated_evidence_targets"])

	var auditEvidenceTargets, auditEvaluatedTargets, auditCreated, auditRejected, auditProviderProposals string
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT after_payload->>'evidence_targets', after_payload->>'evaluated_evidence_targets',
			       after_payload->>'created_dreams', after_payload->>'rejected_dreams',
			       after_payload->>'provider_proposals'
			FROM audit_log
			WHERE team_id = ?::uuid AND entity_type = 'dream_cycle_run' AND entity_id = ?
			ORDER BY timestamp DESC, id DESC
			LIMIT 1
		`, teamID, run.RunID).Row().Scan(
			&auditEvidenceTargets, &auditEvaluatedTargets, &auditCreated, &auditRejected, &auditProviderProposals,
		)
	}))
	require.Equal(t, "1", auditEvidenceTargets)
	require.Equal(t, "1", auditEvaluatedTargets)
	require.Equal(t, "2", auditCreated)
	require.Equal(t, "1", auditRejected)
	require.Equal(t, "5", auditProviderProposals)
}
