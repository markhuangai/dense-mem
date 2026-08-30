package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestRememberAttemptDiagnosticsRepositoryScopesReadsAndPurgesExpiredBytes(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "remember-attempt-diagnostics-a")
	teamB := createLedgerTeam(t, adminDB, rls, "remember-attempt-diagnostics-b")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "remember-attempt-diagnostics-owner-a")
	repo := NewLedgerRepository(appDB, rls)
	attemptID := "00000000-0000-4000-8000-000000000301"
	artifactID := "00000000-0000-4000-8000-000000000302"
	content := []byte(`{"phase":"assessment","error_code":"provider_unavailable"}`)
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt: RememberAttemptRecordInput{
			TeamID: teamA, OwnerProfileID: ownerA, AttemptID: attemptID,
			IdempotencyKey: "diagnostics-scope", RequestHash: "diagnostics-scope-hash",
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed",
			FailedPhase: "assessment", ErrorCode: "provider_unavailable",
			CorrelationID: "diagnostics-correlation", PublicResult: map[string]any{
				"submission_id": attemptID, "secret": "must-not-be-a-list-field",
			},
		},
		Artifacts: []RememberFailureArtifactInput{{ArtifactID: artifactID, ArtifactKind: "failure", ContentType: "application/json", Content: content}},
	}))

	page, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: teamA, Outcome: "failed", Limit: 20})
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	require.Nil(t, page.Records[0].PublicResult)
	require.Empty(t, page.Records[0].Events)
	require.Empty(t, page.Records[0].Artifacts)
	require.Equal(t, attemptID, page.Records[0].AttemptID)

	detail, err := repo.GetRememberAttemptDiagnostic(ctx, teamA, attemptID)
	require.NoError(t, err)
	require.Equal(t, "must-not-be-a-list-field", detail.PublicResult["secret"])
	require.Len(t, detail.Events, 1)
	require.Len(t, detail.Artifacts, 1)

	artifact, err := repo.GetRememberFailureArtifact(ctx, teamA, attemptID, artifactID)
	require.NoError(t, err)
	require.Equal(t, content, artifact.Content)
	var databaseNow time.Time
	require.NoError(t, rls.WithSystemReadOnlyRepeatableTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT clock_timestamp()`).Scan(&databaseNow).Error
	}))
	require.WithinDuration(t, databaseNow.UTC(), artifact.CapturedAt.UTC(), 2*time.Second)
	require.Equal(t, maxRememberFailureArtifactRetention, artifact.ExpiresAt.Sub(artifact.CapturedAt))
	_, err = repo.GetRememberFailureArtifact(ctx, teamB, attemptID, artifactID)
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)

	profileRows := int64(0)
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_failure_artifacts WHERE team_id = ?::uuid`, teamA).Scan(&profileRows).Error
	}))
	require.Zero(t, profileRows)

	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	expiredAttemptID := "00000000-0000-4000-8000-000000000303"
	expiredArtifactID := "00000000-0000-4000-8000-000000000304"
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
		Attempt:   RememberAttemptRecordInput{TeamID: teamA, OwnerProfileID: ownerA, AttemptID: expiredAttemptID, IdempotencyKey: "diagnostics-expired", RequestHash: "diagnostics-expired-hash", ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed", FailedPhase: "assessment", ErrorCode: "provider_unavailable", PublicResult: map[string]any{}},
		Artifacts: []RememberFailureArtifactInput{{ArtifactID: expiredArtifactID, ArtifactKind: "failure", ContentType: "application/json", Content: []byte(`{"expired":true}`), CapturedAt: old, ExpiresAt: old.Add(7 * 24 * time.Hour)}},
	}))
	_, err = repo.GetRememberFailureArtifact(ctx, teamA, expiredAttemptID, expiredArtifactID)
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)
	deleted, err := repo.PurgeExpiredRememberFailureArtifacts(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	remaining, err := repo.GetRememberAttemptDiagnostic(ctx, teamA, expiredAttemptID)
	require.NoError(t, err)
	require.Len(t, remaining.Events, 1)
	require.Empty(t, remaining.Artifacts)
}

func TestRememberFailureArtifactPurgeClaimsConcurrentBatches(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-attempt-diagnostics-concurrent")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-attempt-diagnostics-concurrent-owner")
	repo := NewLedgerRepository(appDB, rls)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	for index := 0; index < 4; index++ {
		attemptID := uuidForDiagnostics(310 + index)
		artifactID := uuidForDiagnostics(320 + index)
		require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{
			Attempt:   RememberAttemptRecordInput{TeamID: teamID, OwnerProfileID: ownerID, AttemptID: attemptID, IdempotencyKey: "concurrent-" + attemptID, RequestHash: "hash-" + attemptID, ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: "failed", FailedPhase: "assessment", ErrorCode: "provider_unavailable", PublicResult: map[string]any{}},
			Artifacts: []RememberFailureArtifactInput{{ArtifactID: artifactID, ArtifactKind: "failure", ContentType: "application/json", Content: []byte(`{"expired":true}`), CapturedAt: old, ExpiresAt: old.Add(7 * 24 * time.Hour)}},
		}))
	}
	results := make(chan int, 2)
	errors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			deleted, err := repo.PurgeExpiredRememberFailureArtifacts(ctx, 2)
			results <- deleted
			errors <- err
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	total := 0
	for deleted := range results {
		total += deleted
	}
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 4, total)
}

func TestRememberAttemptDiagnosticsPaginatesOrdersAndIsolatesABC(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "remember-attempt-diagnostics-pagination-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "remember-attempt-diagnostics-pagination-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "remember-attempt-diagnostics-pagination-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "remember-attempt-diagnostics-pagination-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "remember-attempt-diagnostics-pagination-owner-c")
	repo := NewLedgerRepository(appDB, rls)

	attemptIDs := []string{
		"00000000-0000-4000-8000-000000000401",
		"00000000-0000-4000-8000-000000000402",
		"00000000-0000-4000-8000-000000000403",
		"00000000-0000-4000-8000-000000000404",
		"00000000-0000-4000-8000-000000000405",
	}
	for index, attemptID := range attemptIDs {
		if index > 0 {
			time.Sleep(2 * time.Millisecond)
		}
		ownerID := ownerA
		if index == 2 || index == 4 {
			ownerID = ownerB
		}
		attempt := RememberAttemptRecordInput{
			TeamID: teamA, OwnerProfileID: ownerID, AttemptID: attemptID,
			IdempotencyKey: fmt.Sprintf("diagnostics-pagination-%d", index), RequestHash: fmt.Sprintf("diagnostics-pagination-hash-%d", index),
			ContractVersion: domain.ContractVersion, SubmissionKind: "remember", Outcome: []string{"completed", "rejected", "quarantined", "failed", "failed"}[index],
			CorrelationID: fmt.Sprintf("diagnostics-pagination-correlation-%d", index), PublicResult: map[string]any{"processing_state": "completed", "sequence": index},
			EvidenceCount: index, RelationshipCount: index + 1, DocumentCount: index + 2, AssessorTurns: index + 3, Duration: time.Duration(index+1) * time.Millisecond,
		}
		if attempt.Outcome == "failed" {
			attempt.FailedPhase, attempt.ErrorCode = "assessment", "provider_unavailable"
			input := RememberFailureRecordInput{Attempt: attempt}
			if index == 3 {
				captured := time.Now().UTC()
				input.Artifacts = []RememberFailureArtifactInput{{ArtifactID: "00000000-0000-4000-8000-000000000406", ArtifactKind: "failure", ContentType: "application/json", Content: []byte(`{"safe":true}`), CapturedAt: captured, ExpiresAt: captured.Add(24 * time.Hour)}}
			}
			require.NoError(t, repo.RecordRememberFailure(ctx, input))
			continue
		}
		require.NoError(t, repo.RecordRememberAttempt(ctx, attempt))
	}
	teamCAttempt := RememberAttemptRecordInput{
		TeamID: teamC, OwnerProfileID: ownerC, AttemptID: "00000000-0000-4000-8000-000000000407",
		IdempotencyKey: "diagnostics-pagination-team-c", RequestHash: "diagnostics-pagination-team-c-hash", ContractVersion: domain.ContractVersion,
		SubmissionKind: "remember", Outcome: "completed", PublicResult: map[string]any{"processing_state": "completed"},
	}
	require.NoError(t, repo.RecordRememberAttempt(ctx, teamCAttempt))

	require.NoError(t, insertRememberDiagnosticEvents(ctx, adminDB, rls, teamA, ownerA, attemptIDs[3], []map[string]any{
		{"sequence_no": 2, "phase": "assessment", "event_kind": "assessment_retry", "outcome": "failed", "metadata": map[string]any{"markup": "<script>bad()</script>"}},
		{"sequence_no": 3, "phase": "commit", "event_kind": "failure_recorded", "outcome": "failed", "metadata": map[string]any{"retained": true}},
	}))

	full, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: teamA, Limit: 100})
	require.NoError(t, err)
	require.Equal(t, int64(5), full.Total)
	require.Len(t, full.Records, 5)
	for index, record := range full.Records {
		require.Equal(t, teamA, record.TeamID)
		require.NotEqual(t, teamC, record.TeamID)
		require.Nil(t, record.PublicResult)
		require.Empty(t, record.Events)
		require.Empty(t, record.Artifacts)
		if index > 0 {
			previous := full.Records[index-1]
			require.True(t, !record.CreatedAt.After(previous.CreatedAt), "records must be ordered newest first")
		}
	}
	require.Contains(t, []string{ownerA, ownerB}, full.Records[0].OwnerProfileID)

	pageOne, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: teamA, Limit: 2, Offset: 0})
	require.NoError(t, err)
	pageTwo, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: teamA, Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Equal(t, int64(5), pageOne.Total)
	require.Equal(t, int64(5), pageTwo.Total)
	require.Len(t, pageOne.Records, 2)
	require.Len(t, pageTwo.Records, 2)
	require.Equal(t, full.Records[0].AttemptID, pageOne.Records[0].AttemptID)
	require.Equal(t, full.Records[1].AttemptID, pageOne.Records[1].AttemptID)
	require.Equal(t, full.Records[2].AttemptID, pageTwo.Records[0].AttemptID)
	require.Equal(t, full.Records[3].AttemptID, pageTwo.Records[1].AttemptID)

	failed, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: teamA, Outcome: "failed", Limit: 100})
	require.NoError(t, err)
	require.Len(t, failed.Records, 2)

	global, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{Limit: 100})
	require.NoError(t, err)
	require.Equal(t, int64(6), global.Total)
	require.Len(t, global.Records, 6)
	for index, record := range global.Records {
		require.Contains(t, []string{teamA, teamC}, record.TeamID)
		require.Nil(t, record.PublicResult)
		require.Empty(t, record.Events)
		require.Empty(t, record.Artifacts)
		if index == 0 {
			continue
		}
		previous := global.Records[index-1]
		if record.CreatedAt.Equal(previous.CreatedAt) {
			require.Less(t, record.AttemptID, previous.AttemptID, "equal timestamps must use descending attempt_id order")
			continue
		}
		require.True(t, record.CreatedAt.Before(previous.CreatedAt), "global records must be ordered newest first")
	}

	globalPageOne, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: "", Limit: 2, Offset: 0})
	require.NoError(t, err)
	globalPageTwo, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: "", Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Equal(t, int64(6), globalPageOne.Total)
	require.Equal(t, int64(6), globalPageTwo.Total)
	require.Len(t, globalPageOne.Records, 2)
	require.Len(t, globalPageTwo.Records, 2)
	require.Equal(t, global.Records[0].AttemptID, globalPageOne.Records[0].AttemptID)
	require.Equal(t, global.Records[1].AttemptID, globalPageOne.Records[1].AttemptID)
	require.Equal(t, global.Records[2].AttemptID, globalPageTwo.Records[0].AttemptID)
	require.Equal(t, global.Records[3].AttemptID, globalPageTwo.Records[1].AttemptID)

	globalFailed, err := repo.ListRememberAttemptDiagnostics(ctx, RememberAttemptDiagnosticFilter{TeamID: "", Outcome: "failed", Limit: 100})
	require.NoError(t, err)
	require.Equal(t, int64(2), globalFailed.Total)
	require.Len(t, globalFailed.Records, 2)
	for _, record := range globalFailed.Records {
		require.Equal(t, "failed", record.Outcome)
		require.Nil(t, record.PublicResult)
		require.Empty(t, record.Events)
		require.Empty(t, record.Artifacts)
	}

	detail, err := repo.GetRememberAttemptDiagnostic(ctx, teamA, attemptIDs[3])
	require.NoError(t, err)
	require.Len(t, detail.Events, 3)
	require.Equal(t, []int{1, 2, 3}, []int{detail.Events[0].SequenceNo, detail.Events[1].SequenceNo, detail.Events[2].SequenceNo})
	require.Equal(t, "<script>bad()</script>", detail.Events[1].Metadata["markup"])
	require.Len(t, detail.Artifacts, 1)
	require.Equal(t, "sha256:13f513fe32a8991557ebf28941b75597641e94717c08569b7723d998c7428423", detail.Artifacts[0].ContentSHA256)
	require.False(t, detail.Artifacts[0].ExpiresAt.IsZero())

	_, err = repo.GetRememberAttemptDiagnostic(ctx, teamC, attemptIDs[3])
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticNotFound)
	_, err = repo.GetRememberAttemptDiagnostic(ctx, teamA, "00000000-0000-4000-8000-000000000499")
	require.ErrorIs(t, err, ErrRememberAttemptDiagnosticNotFound)
	_, err = repo.GetRememberFailureArtifact(ctx, teamC, attemptIDs[3], "00000000-0000-4000-8000-000000000406")
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)
	_, err = repo.GetRememberFailureArtifact(ctx, teamA, "00000000-0000-4000-8000-000000000499", "00000000-0000-4000-8000-000000000406")
	require.ErrorIs(t, err, ErrRememberFailureArtifactNotFound)

	var sameTeamVisible, crossTeamVisible int64
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, teamA, ownerB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid`, teamA).Scan(&sameTeamVisible).Error
	}))
	require.Equal(t, int64(5), sameTeamVisible)
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, teamC, ownerC, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid`, teamA).Scan(&crossTeamVisible).Error
	}))
	require.Zero(t, crossTeamVisible)
}

func insertRememberDiagnosticEvents(ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, teamID, ownerID, attemptID string, events []map[string]any) error {
	return rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		for _, event := range events {
			metadata, err := json.Marshal(event["metadata"])
			if err != nil {
				return err
			}
			if err := tx.WithContext(ctx).Exec(`
				INSERT INTO remember_attempt_events (team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind, outcome, metadata)
				VALUES (?::uuid, gen_random_uuid(), ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb)
			`, teamID, attemptID, ownerID, event["sequence_no"], event["phase"], event["event_kind"], event["outcome"], string(metadata)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func uuidForDiagnostics(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", value)
}
