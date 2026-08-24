package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
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
}

func TestPlacementStatusHidesSealedPrivateGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "placement-status-private-generation"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, ownerID, "private-status", domain.CredentialBindingCredentialPrivate)
	ledgerRepo := NewLedgerRepository(appDB, rls)

	created, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  target.ID.String(),
		SpaceID:         target.MemorySpaceID.String(),
		SpaceGeneration: target.MemorySpaceGeneration,
		IdempotencyKey:  "placement-status-private-generation",
		RequestHash:     sha256Hex("private status generation"),
		Evidence:        []EvidenceInput{{Content: "sealed private placement status must be hidden"}},
	})
	require.NoError(t, err)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE memory_spaces
			SET generation = generation + 1, lifecycle_state = 'sealed', sealed_at = now()
			WHERE id = ? AND team_id = ? AND lifecycle_state = 'active'
		`, target.MemorySpaceID, teamID)
		require.Equal(t, int64(1), result.RowsAffected)
		return result.Error
	}))

	_, err = ledgerRepo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         teamID.String(),
		OwnerProfileID: target.ID.String(),
		IngestID:       created.IngestID,
	})
	require.ErrorIs(t, err, ErrPlacementNotFound)

	var retained int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM placement_runs
			WHERE team_id = ? AND ingest_id = ?
		`, teamID, created.IngestID).Row().Scan(&retained)
	}))
	require.Equal(t, int64(1), retained)
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

	create := func(teamID, ownerID, key, content string, sourceTypes ...string) *CreateIngestResult {
		if len(sourceTypes) == 0 {
			sourceTypes = []string{"conversation"}
		}
		evidence := make([]EvidenceInput, 0, len(sourceTypes))
		for _, sourceType := range sourceTypes {
			evidence = append(evidence, EvidenceInput{Content: content, SourceType: sourceType})
		}
		result, err := repo.CreateIngest(ctx, CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key,
			RequestHash: sha256Hex(content),
			Metadata:    map[string]any{"actor": map[string]any{"correlation_id": "corr-" + key}},
			Evidence:    evidence,
		})
		require.NoError(t, err)
		return result
	}
	failed := create(teamA, ownerA, "failed", "private failed evidence", "document")
	rejected := create(teamA, ownerA, "rejected", "private rejected evidence", "document")
	queued := create(teamA, ownerB, "queued", "private queued evidence", "manual", "conversation", "manual")
	foreign := create(teamC, ownerC, "foreign", "private foreign evidence", "observation")
	hostileSourceSummary := `{"password":"supersecret","access_token":"opaque-secret"}`
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE knowledge_ingests
			SET source_summary = ?
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, hostileSourceSummary, teamA, failed.IngestID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'failed', category = 'failed',
			    result = jsonb_build_object(
			        'failure_stage', 'assessment',
			        'failure_class', 'timeout',
			        'failure_code', 'normalizer_unavailable',
			        'provider_response', 'must-not-cross-diagnostics-boundary'
			    )
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, failed.IngestID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_runs
			SET status = 'failed', attempts = max_attempts,
			    completed_at = now(), updated_at = now(), lease_until = NULL
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, failed.IngestID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE placement_items
			SET status = 'rejected', category = 'rejected',
			    result = jsonb_build_object('failure_code', 'no_supported_memory')
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, rejected.IngestID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE placement_runs
			SET status = 'rejected', completed_at = now(), updated_at = now(), lease_until = NULL
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, rejected.IngestID).Error
	}))
	duplicateAt := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	terminalAt := duplicateAt.Add(10 * time.Minute)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO placement_outcomes (
			    team_id, placement_run_id, owner_profile_id,
			    outcome_kind, status, idempotency_key, payload, created_at
			)
			SELECT
			    ?::uuid, ?::uuid, ?::uuid,
			    'submission_assessment_terminal', 'failed', '',
			    jsonb_build_object(
			        'failure_reason_code', 'provider_response_invalid',
			        'failure_stage', 'assessment',
			        'failure_class', 'validation_failed'
			    ), ?
			FROM generate_series(1, 20)
		`, teamA, failed.PlacementRunID, ownerA, terminalAt).Error; err != nil {
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
			           'assessor_turns', series.assessor_turns,
			           'provider_response', 'must-not-cross-diagnostics-boundary'
			       ), ?::timestamptz + (series.assessor_turns * interval '1 second')
			FROM generate_series(1, 201) AS series(assessor_turns)
		`, teamA, failed.PlacementRunID, ownerA, duplicateAt).Error
	}))

	all, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(4), all.Total)
	require.Len(t, all.Records, 4)

	teamOnly, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{TeamID: teamA, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), teamOnly.Total)
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
	assert.Equal(t, []string{"document"}, failedOnly.Records[0].SourceTypes)
	listJSON, err := json.Marshal(failedOnly.Records[0])
	require.NoError(t, err)
	assert.NotContains(t, string(listJSON), "supersecret")
	assert.NotContains(t, string(listJSON), "opaque-secret")
	assert.Equal(t, "normalizer_unavailable", failedOnly.Records[0].Placement.Items[0].Result["failure_code"])

	rejectedOnly, err := repo.ListSubmissionDiagnostics(ctx, SubmissionDiagnosticFilter{
		TeamID: teamA, ProcessingState: "rejected", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rejectedOnly.Total)
	require.Len(t, rejectedOnly.Records, 1)
	assert.Equal(t, rejected.IngestID, rejectedOnly.Records[0].Placement.IngestID)
	assert.Equal(t, string(domain.PlacementRunRejected), rejectedOnly.Records[0].Placement.Status)

	detail, err := repo.GetSubmissionDiagnostic(ctx, teamA, failed.IngestID)
	require.NoError(t, err)
	assert.Equal(t, []string{"document"}, detail.SourceTypes)
	detailJSON, err := json.Marshal(detail)
	require.NoError(t, err)
	assert.NotContains(t, string(detailJSON), "supersecret")
	assert.NotContains(t, string(detailJSON), "opaque-secret")
	require.Len(t, detail.Placement.Evidence, 1)
	assert.Empty(t, detail.Placement.Evidence[0].Content)
	require.Len(t, detail.Placement.Items, 1)
	assert.NotContains(t, detail.Placement.Items[0].Result, "provider_response")
	assert.Equal(t, "assessment", detail.Placement.Items[0].Result["failure_stage"])
	assert.Equal(t, "normalizer_unavailable", detail.Placement.Items[0].Result["failure_code"])
	assert.Len(t, detail.OperatorDiagnostics, 200)
	assert.Equal(t, "assessor_provider_failed", detail.OperatorDiagnostics[0].Payload["failure_reason_code"])
	assert.Equal(t, float64(3), detail.OperatorDiagnostics[0].Payload["assessor_turns"])
	assert.Equal(t, float64(201), detail.OperatorDiagnostics[198].Payload["assessor_turns"])
	assert.Equal(t, "provider_response_invalid", detail.OperatorDiagnostics[199].Payload["failure_reason_code"])
	assert.NotContains(t, detail.OperatorDiagnostics[0].Payload, "provider_response")

	_, err = repo.GetSubmissionDiagnostic(ctx, teamC, failed.IngestID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
	_, err = repo.GetSubmissionDiagnostic(ctx, teamA, foreign.IngestID)
	require.ErrorIs(t, err, ErrSubmissionDiagnosticNotFound)
	queuedDetail, err := repo.GetSubmissionDiagnostic(ctx, teamA, queued.IngestID)
	require.NoError(t, err)
	assert.Equal(t, []string{"conversation", "manual"}, queuedDetail.SourceTypes)
	assert.Equal(t, 3, queuedDetail.EvidenceCount)
}
