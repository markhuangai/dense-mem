package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubmissionDiagnosticsLoadsEventsAndArtifactsOnlyForDetail(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	teamID := createLedgerTeam(t, adminDB, rls, "remember-diagnostics-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-diagnostics-owner")
	ledger := NewLedgerRepository(appDB, rls)
	attemptID := uuid.NewString()

	require.NoError(t, ledger.RecordRememberFailure(context.Background(), RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: attemptID,
		IdempotencyKey: "diagnostics-key", RequestHash: "diagnostics-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "embedding", ErrorCode: "embedding_unavailable",
		PublicResult: map[string]any{
			"contract_version": "dense-mem.v2.6.1", "submission_id": attemptID,
			"submission_kind": "remember", "processing_state": "failed",
			"search_state": "not_required", "evidence": []any{},
			"relationship_results": []any{}, "errors": []any{},
		},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactKind: "request", ContentType: "application/json", Content: []byte(`{"phase":"embedding"}`),
			CapturedAt: time.Now().UTC(),
		}},
	}))

	page, err := ledger.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{TeamID: teamID})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Empty(t, page.Records[0].Events)
	require.Empty(t, page.Records[0].Artifacts)

	detail, err := ledger.GetSubmissionDiagnostic(context.Background(), teamID, attemptID)
	require.NoError(t, err)
	require.Len(t, detail.Events, 1)
	require.Len(t, detail.Artifacts, 1)
	require.Equal(t, "request", detail.Artifacts[0].ArtifactKind)
}

func TestSubmissionDiagnosticsAllowsAllTeamFilter(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	teamID := createLedgerTeam(t, adminDB, rls, "remember-diagnostics-all-teams")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-diagnostics-all-teams-owner")
	ledger := NewLedgerRepository(appDB, rls)
	attemptID := uuid.NewString()
	require.NoError(t, ledger.RecordRememberFailure(context.Background(), RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: attemptID,
		IdempotencyKey: "diagnostics-all-teams-key", RequestHash: "diagnostics-all-teams-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "assessment", ErrorCode: "provider_unavailable",
		PublicResult: map[string]any{"submission_id": attemptID, "processing_state": "failed"},
	}))

	page, err := ledger.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Equal(t, attemptID, page.Records[0].SubmissionID)
}

func TestRememberAttemptAndArtifactPoliciesAreOwnerAndControlScoped(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-policy-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "remember-policy-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "remember-policy-owner-b")
	ledger := NewLedgerRepository(appDB, rls)
	attemptID := uuid.NewString()
	require.NoError(t, ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerA, AttemptID: attemptID,
		IdempotencyKey: "policy-key", RequestHash: "policy-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "assessment", ErrorCode: "provider_unavailable",
		PublicResult: map[string]any{"submission_id": attemptID},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactKind: "failure", ContentType: "application/json", Content: []byte(`{"safe":true}`),
		}},
	}))

	var ownerAttempts, otherAttempts, ownerArtifacts, otherArtifacts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID).Scan(&ownerAttempts).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_failure_artifacts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID).Scan(&ownerArtifacts).Error
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID).Scan(&otherAttempts).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_failure_artifacts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID).Scan(&otherArtifacts).Error
	}))
	require.EqualValues(t, 1, ownerAttempts)
	require.Zero(t, otherAttempts)
	require.Zero(t, ownerArtifacts)
	require.Zero(t, otherArtifacts)

	var updatedHash string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		update := tx.Exec(`UPDATE remember_attempts SET request_hash = 'tampered' WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 0 {
			return errors.New("owner B unexpectedly updated an attempt")
		}
		result := tx.Exec(`DELETE FROM remember_attempts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 0 {
			return errors.New("owner B unexpectedly deleted an attempt")
		}
		return nil
	}))
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT request_hash FROM remember_attempts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID).Row().Scan(&updatedHash)
	}))
	require.Equal(t, "policy-hash", updatedHash)
}

func TestPurgeExpiredRememberFailureArtifactsUsesAuditedSystemPath(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-artifact-purge-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-artifact-purge-owner")
	ledger := NewLedgerRepository(appDB, rls)
	now := time.Now().UTC().Truncate(time.Microsecond)
	attemptID := uuid.NewString()
	require.NoError(t, ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: attemptID,
		IdempotencyKey: "purge-key", RequestHash: "purge-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "embedding", ErrorCode: "embedding_unavailable",
		PublicResult: map[string]any{"submission_id": attemptID},
		Artifacts: []RememberFailureArtifactInput{{
			ArtifactKind: "failure", ContentType: "application/json", Content: []byte(`{"expired":true}`),
			CapturedAt: now.Add(-7 * 24 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		}},
	}))

	deleted, err := ledger.PurgeExpiredRememberFailureArtifacts(ctx, now, 100)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)

	var remaining int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_failure_artifacts WHERE team_id = ?::uuid AND attempt_id = ?::uuid`, teamID, attemptID).Scan(&remaining).Error
	}))
	require.Zero(t, remaining)
}

func TestPurgeExpiredRememberFailureArtifactsDrainsBatches(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-artifact-purge-drain-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-artifact-purge-drain-owner")
	ledger := NewLedgerRepository(appDB, rls)
	now := time.Now().UTC().Truncate(time.Microsecond)
	artifacts := make([]RememberFailureArtifactInput, rememberFailureArtifactPurgeBatchSize+1)
	for index := range artifacts {
		artifacts[index] = RememberFailureArtifactInput{
			ArtifactID:   uuid.NewString(),
			ArtifactKind: "failure",
			ContentType:  "application/json",
			Content:      []byte(`{"expired":true}`),
			CapturedAt:   now.Add(-7 * 24 * time.Hour),
			ExpiresAt:    now.Add(-time.Hour),
		}
	}
	require.NoError(t, ledger.RecordRememberFailure(ctx, RememberFailureRecordInput{
		TeamID: teamID, OwnerProfileID: ownerID, AttemptID: uuid.NewString(),
		IdempotencyKey: "purge-drain-key", RequestHash: "purge-drain-hash",
		ContractVersion: "dense-mem.v2.6.1", SubmissionKind: "remember",
		FailedPhase: "embedding", ErrorCode: "embedding_unavailable", Artifacts: artifacts,
	}))

	deleted, err := ledger.purgeExpiredRememberFailureArtifactsUntilDrained(ctx, now)
	require.NoError(t, err)
	require.Equal(t, len(artifacts), deleted)
}
