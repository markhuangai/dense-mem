package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlacementStatusReadsCurrentOwnerScopedItemVersions(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "placement-status-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "owner-b")
	ledgerRepo := NewLedgerRepository(appDB, rls)

	ingest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerA,
		"placement-status", "Placement status should expose current item version.")
	submittedAt := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	startedAt := submittedAt.Add(time.Second)
	updatedAt := submittedAt.Add(30 * time.Second)
	nextAttemptAt := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE knowledge_ingests
			SET metadata = jsonb_set(
			        metadata,
			        '{actor}',
			        jsonb_build_object('correlation_id', ?::text),
			        true
			    ),
			    created_at = ?
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, "corr-placement-status", submittedAt, teamID, ingest.IngestID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE placement_runs
			SET attempts = 2, max_attempts = 5, available_at = ?, created_at = ?,
			    started_at = ?, updated_at = ?
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, nextAttemptAt, submittedAt, startedAt, updatedAt, teamID, ingest.IngestID).Error
	}))
	status, err := ledgerRepo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		IngestID:       ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, status.Items, 1)
	assert.Equal(t, ingest.PlacementRunID, status.PlacementRunID)
	assert.Equal(t, "queued", status.Status)
	assert.Equal(t, "corr-placement-status", status.CorrelationID)
	assert.Equal(t, 2, status.Attempts)
	assert.Equal(t, 5, status.MaxAttempts)
	assert.Equal(t, submittedAt, *status.SubmittedAt)
	assert.Equal(t, nextAttemptAt, *status.NextAttemptAt)
	assert.Equal(t, startedAt, *status.StartedAt)
	assert.Equal(t, updatedAt, *status.UpdatedAt)
	assert.Nil(t, status.CompletedAt)
	assert.Equal(t, 1, status.Items[0].Version)
	assert.Equal(t, "queued", status.Items[0].Status)

	_, err = ledgerRepo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerB,
		IngestID:       ingest.IngestID,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPlacementNotFound), err)

	_, err = ledgerRepo.ResolvePlacementReview(ctx, ResolvePlacementReviewInput{
		TeamID:               teamID,
		OwnerProfileID:       ownerA,
		Action:               "reject",
		IngestID:             ingest.IngestID,
		PlacementItemID:      ingest.Items[0].PlacementItemID,
		PlacementItemVersion: ingest.Items[0].Version,
		Message:              "not durable memory",
		IdempotencyKey:       "reject-status",
	})
	require.NoError(t, err)

	status, err = ledgerRepo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID,
		OwnerProfileID: ownerA,
		IngestID:       ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, status.Items, 1)
	assert.Equal(t, "completed", status.Status)
	assert.Equal(t, 2, status.Items[0].Version)
	assert.Equal(t, "completed", status.Items[0].Status)
	assert.Equal(t, "candidate", status.Items[0].Category)
}

func TestSubmissionDiagnosticsUseSystemScopeButHonorExactTeamFilter(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "submission-diagnostics-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "submission-diagnostics-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "owner-c")
	repo := NewLedgerRepository(appDB, rls)

	create := func(teamID, ownerID, key, content string) *CreateIngestResult {
		result, err := repo.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key,
			RequestHash: sha256Hex(content),
			Metadata:    map[string]any{"actor": map[string]any{"correlation_id": "corr-" + key}},
			Evidence:    []EvidenceInput{{Content: content}},
		})
		require.NoError(t, err)
		return result
	}
	failed := create(teamA, ownerA, "failed", "private failed evidence")
	queued := create(teamA, ownerB, "queued", "private queued evidence")
	foreign := create(teamC, ownerC, "foreign", "private foreign evidence")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'failed', category = 'failed',
			    result = jsonb_build_object(
			        'failure_stage', 'assessment',
			        'failure_class', 'timeout',
			        'provider_response', 'must-not-cross-diagnostics-boundary'
			    )
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, failed.IngestID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE placement_runs
			SET status = 'failed', attempts = max_attempts,
			    completed_at = now(), updated_at = now(), lease_until = NULL
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, failed.IngestID).Error
	}))
	duplicateAt := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	distinctAt := duplicateAt.Add(-time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO placement_outcomes (
			    team_id, placement_run_id, owner_profile_id,
			    outcome_kind, status, idempotency_key, payload, created_at
			)
			VALUES (
			    ?::uuid, ?::uuid, ?::uuid,
			    'submission_assessment_terminal', 'failed', '',
			    jsonb_build_object(
			        'failure_reason_code', 'assessor_response_invalid',
			        'failure_stage', 'assessment',
			        'failure_class', 'validation_failed'
			    ), ?
			)
		`, teamA, failed.PlacementRunID, ownerA, distinctAt).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO placement_outcomes (
			    team_id, placement_run_id, owner_profile_id,
			    outcome_kind, status, idempotency_key, payload, created_at
			)
			SELECT ?::uuid, ?::uuid, ?::uuid,
			       'submission_assessment_attempt', 'retryable', '',
			       jsonb_build_object(
			           'failure_reason_code', 'assessor_provider_failed',
			           'failure_stage', 'assessment',
			           'failure_class', 'timeout',
			           'provider_response', 'must-not-cross-diagnostics-boundary'
			       ), ?
			FROM generate_series(1, 201)
		`, teamA, failed.PlacementRunID, ownerA, duplicateAt).Error
	}))

	all, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), all.Total)
	require.Len(t, all.Records, 3)

	teamOnly, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{TeamID: teamA, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), teamOnly.Total)
	for _, record := range teamOnly.Records {
		assert.Equal(t, teamA, record.Placement.TeamID)
	}

	failedOnly, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{
		TeamID: teamA, ProcessingState: "failed", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), failedOnly.Total)
	require.Len(t, failedOnly.Records, 1)
	assert.Equal(t, failed.IngestID, failedOnly.Records[0].Placement.IngestID)
	assert.Equal(t, "corr-failed", failedOnly.Records[0].Placement.CorrelationID)

	detail, err := repo.GetSubmissionDiagnostic(ctx, teamA, failed.IngestID)
	require.NoError(t, err)
	require.Len(t, detail.Placement.Evidence, 1)
	assert.Empty(t, detail.Placement.Evidence[0].Content)
	require.Len(t, detail.Placement.Items, 1)
	assert.NotContains(t, detail.Placement.Items[0].Result, "provider_response")
	assert.Equal(t, "assessment", detail.Placement.Items[0].Result["failure_stage"])
	assert.Len(t, detail.OperatorDiagnostics, 2)
	assert.Equal(t, "assessor_response_invalid", detail.OperatorDiagnostics[0].Payload["failure_reason_code"])
	assert.Equal(t, "assessor_provider_failed", detail.OperatorDiagnostics[1].Payload["failure_reason_code"])
	assert.NotContains(t, detail.OperatorDiagnostics[1].Payload, "provider_response")

	_, err = repo.GetSubmissionDiagnostic(ctx, teamC, failed.IngestID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
	_, err = repo.GetSubmissionDiagnostic(ctx, teamA, foreign.IngestID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
	_, err = repo.GetSubmissionDiagnostic(ctx, teamA, queued.IngestID)
	require.NoError(t, err)
}
